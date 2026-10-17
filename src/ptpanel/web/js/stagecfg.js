"use strict";

// The functional / aging / user-level configurations (decision 97, STG-26,
// STG-27). Kept by the panel in porttool_stagecfg.json via /api/stagecfg;
// every change is saved on the spot and sends nothing to the board.
// PORTTOOL-FLOW.md C.3.6 "三种配置".

const CFG_VIEWS = ["func", "aging", "user"];
let stageCfgs = null;     // the last /api/stagecfg reply
let stageCfgLoading = false;

const isCfgView = v => CFG_VIEWS.includes(v);
const cfgOf = v => (stageCfgs && stageCfgs[v]) || {};
const cfgItem = (v, id) => (cfgOf(v).items || {})[id] || {};

async function loadStageCfgs() {
  if (stageCfgLoading) { return; }
  stageCfgLoading = true;
  try { stageCfgs = await api("/api/stagecfg"); } finally { stageCfgLoading = false; }
  renderShell();
}

// Same rule as stageCfg.on in stagecfg.go.
function cfgOn(v, it) {
  const c = cfgItem(v, it.id);
  if (c.on !== undefined) { return c.on; }
  if (it.default_off) { return false; }   // the reference board cannot test it as wired by default (defaultOff, decisions 129, 135)
  return !it.exclusive || itemsOf(v, it.port).find(o => o === it || excludes(v, o, it)) === it; // shell.js
}
const cfgRefUnused = () => !!(stageCfgs && stageCfgs.ref_unused);
// AI / AO / DI / DO run against the reference board outside R&D, never a
// person; one with no such route is refused at the start (decisions 129, 130; fixedRef in stagecfg.go).
const cfgFixedRef = it => ["ain", "aout", "din", "dout", "relay"].includes(it.port);
const cfgPeer = (v, it) => cfgFixedRef(it) ? (it.peers.includes("ref") ? "ref" : it.peers[0])
  : cfgItem(v, it.id).peer || (cfgRefUnused() ? it.peers[0] : cfgDefaultPeer(it));
// Same rule as ptitems.DefaultPeer (decision 112): an item that offers the
// reference board takes it while it is connected, else its other peer.
function cfgDefaultPeer(it) {
  const refOn = typeof connInfo === "object" && (connInfo.ref || {}).connected;
  if (it.peers.includes("ref") && !refOn) { return it.peers.find(p => p !== "ref") || it.peers[0]; }
  return it.peers[0];
}
const cfgPortOn = (v, port) => !(cfgOf(v).ports_off || {})[port];
const cfgDuration = v => cfgOf(v).duration || "once";
// A sub-test's time in seconds: "10m" / "30m", else whole hours (stageDurations in stagecfg.go).
const cfgSeconds = v => { const d = cfgDuration(v); return d === "once" ? 0 : /m$/.test(d) ? Number(d.slice(0, -1)) * 60 : Number(d) * 3600; };

// Applies one change to a copy, refuses it when the timed part would overrun
// the control port, else saves it; a refusal leaves the saved config as it was.
async function cfgChange(v, mutate) {
  const next = JSON.parse(JSON.stringify(cfgOf(v)));
  next.items = next.items || {};
  next.ports_off = next.ports_off || {};
  const note = mutate(next);
  const warn = cfgBandwidthWarning(v, next);
  if (warn) { showMessage(warn.replace(/\n/g, " "), true); renderShell(); return; }
  const j = await api("/api/stagecfg", { stage: v, cfg: next });
  stageCfgs = j;
  showMessage(j.error || note || "", !!j.error);
  renderShell();
}

function cfgTick(v, it, on) {
  cfgChange(v, c => {
    (c.items[it.id] = c.items[it.id] || {}).on = on;
    if (!on || !it.exclusive) { return ""; }
    // One session cannot run in two modes at once (decision 102).
    const dropped = [];
    for (const o of itemsOf(v, it.port)) {
      if (excludes(v, o, it) && cfgOn(v, o)) {
        (c.items[o.id] = c.items[o.id] || {}).on = false;
        dropped.push(o.name);
      }
    }
    return dropped.length ? T("cfg.excl_dropped", { list: dropped.join(T("shell.list_sep")), item: it.name }) : "";
  });
}
const cfgSetPeer = (v, it, peer) => cfgChange(v, c => { (c.items[it.id] = c.items[it.id] || {}).peer = peer; });
const cfgSetDuration = (v, d) => cfgChange(v, c => { c.duration = d; });
const cfgSetPort = (v, port, on) => cfgChange(v, c => { if (on) { delete c.ports_off[port]; } else { c.ports_off[port] = true; } });
function cfgSetParam(v, it, k, val) {
  cfgChange(v, c => {
    const ic = c.items[it.id] = c.items[it.id] || {};
    ic.params = ic.params || {};
    if (val === "") { delete ic.params[k]; } else { ic.params[k] = val; }
  });
}

// One AI / AO channel's range, shared by the three configurations; the server
// drops that channel's test points so they follow the new range (decision 139).
async function cfgSetRange(ch, r) {
  const j = await api("/api/stagecfg", { ranges: { [ch]: r } });
  stageCfgs = j;
  // A red, unsaved point typed on the old range no longer applies either.
  const ids = catalog.items.filter(it => (it.port === "ain" || it.port === "aout") && it.channel > 0 && anaChannel(it) === ch).map(it => it.id);
  for (const v of CFG_VIEWS) { for (const id of ids) { delete memoDraft[v + "#pts|" + id]; } }
  showMessage(j.error || "", !!j.error);
  renderShell();
}

// AI / AO test points; empty = the defaults (C.3.2). Not a firmware parameter.
function cfgSetPoints(v, it, pts) {
  cfgChange(v, c => {
    const ic = c.items[it.id] = c.items[it.id] || {};
    if (pts.length) { ic.points = pts; } else { delete ic.points; }
  });
}

// One session frame, its pt.echo and the OK all share the 115200 control line;
// a full line blocks printf and every port's sample period drifts silently.
const LINE_BYTES_PER_SEC = 11520;   // 115200 8N1
const FRAME_BYTES = 90, ECHO_BYTES = 16, OK_BYTES = 20;

// With a duration the ticked sessions run together and must fit 80 % of the
// line (PORTTOOL-FLOW.md C.3.6 "带宽"); bandwidthOf is the bytes per second.
function bandwidthOf(period) {
  let bytes = 0;
  for (const [name, ms] of Object.entries(period)) {
    const p = (state.ports || []).find(x => x.name === name);
    if (!p || p.kind !== "session") { continue; }
    bytes += ((p.loop === "ctrl" || p.loop === "self") ? FRAME_BYTES + ECHO_BYTES + OK_BYTES : FRAME_BYTES) * 1000 / ms;
  }
  return bytes;
}

function cfgBandwidthWarning(v, c) {
  if ((c.duration || "once") === "once") { return ""; }
  const period = {};
  let line = LINE_BYTES_PER_SEC;   // RS232 at another rate slows the whole line (B.7)
  for (const it of catalog.items) {
    if (!it.stages.includes(v) || (c.ports_off || {})[it.port] || it.once) { continue; }
    const ic = (c.items || {})[it.id] || {};
    const on = ic.on !== undefined ? ic.on : cfgOn(v, it);
    if (!on || cfgEffPeer(v, it) === "human") { continue; }
    if (it.port === "rs232" && Number((ic.params || {}).baud) > 0) { line = Math.min(line, Number(ic.params.baud) / 10); }
    for (const tg of it.targets) {
      if (!tg.session) { continue; }
      const want = Number((ic.params || {}).period);
      const p = (state.ports || []).find(x => x.name === tg.session);
      const board = Number(p && p.values && p.values.period);
      const ms = want >= 50 ? want : board >= 50 ? board : 200;
      period[tg.session] = Math.min(period[tg.session] || Infinity, ms);
    }
  }
  const bytes = bandwidthOf(period);
  if (bytes <= line * 0.8) { return ""; }
  return T("ui.bandwidthWarning.1", { pct: Math.round(bytes * 100 / line), used: Math.round(bytes), line_bytes_per_sec: line });
}

// The editable parameters of an item on a configuration page; empty = the board's value.
function cfgParamEditor(v, it) {
  const box = el("div", "cfgparams");
  if (!(it.params || []).length) { box.appendChild(el("div", null, T("shell.item.noparams"))); return box; }
  const p = (state.ports || []).find(x => x.name === it.port);
  const set = cfgItem(v, it.id).params || {};
  for (const k of it.params) {
    const lb = el("label", null, paramLabel(it, k) + " ");
    const inp = paramField(it.port, k, set[k] || "",
      String(((libData.defaults || {})[it.id] || {})[k] ?? ((p && p.values) || {})[k] ?? ""));
    inp.dataset.cfgparam = it.id + "|" + k;
    if (inp.tagName === "INPUT") {
      // A bad value stays red in the cell and is not sent (memo.js, C.3.6 「填数超量程」).
      lb.appendChild(memoCell(inp, "par|" + it.id + "|" + k, () => set[k] || "", () => {}));
      inp.onchange = () => { if (!inp.classList.contains("badcell")) { cfgSetParam(v, it, k, inp.value.trim()); } };
    } else {
      inp.onchange = () => cfgSetParam(v, it, k, inp.value.trim());
      lb.appendChild(inp);
    }
    box.appendChild(lb);
  }
  return box;
}

// What the card says when the reference board is unused and this item needed it (STG-27).
function cfgSubstNote(v, it) {
  const s = stageCfgs && stageCfgs.ref_unused && (stageCfgs.subst || {})[it.id];
  if (!s || cfgPeer(v, it) !== "ref") { return null; }
  const d = el("div", "hint warn", T("cfg.subst", { note: s.note }));
  d.dataset.subst = it.id;
  return d;
}

// The peer an item runs with in this configuration: with the reference board
// unused, what it is swapped for: a person or the PC, no self-loop (decision 135).
function cfgEffPeer(v, it) {
  const p = cfgPeer(v, it);
  const s = p === "ref" && cfgRefUnused() && (stageCfgs.subst || {})[it.id];
  return s ? s.peer : p;
}

// What a person gives for an item with a person as the peer; "" when nothing.
function cfgHumanText(it, peer) {
  if (peer !== "human") { return ""; }
  if (it.live) { return ""; }
  return T(it.human === "number" ? "shell.item.human_number" : "shell.item.human_yesno");
}

// How the item runs in this sub-test; the same split as runConfig
// (stagecfg.go) and ptlib's timed part (PORTTOOL-FLOW.md C.3.6
// 「功能 / 老化 / 用户级有什么不同」).
function cfgRunText(v, it, peer) {
  const timed = cfgDuration(v) !== "once", d = durLabel(cfgDuration(v));
  if (it.id === "relay.cycle") { return timed ? T("cfg.run.relay_timed", { d }) : T("cfg.run.single"); }
  // The DO chain runs before the relays share it (decision 133, ptlib Engine.Run).
  if (it.id === "dout.load" && peer === "ref") { return timed ? T("cfg.run.chain_first") : T("cfg.run.single"); }
  if (it.load && timed) { return T("cfg.run.load_timed", { d }); }
  if (!timed) { return T("cfg.run.single"); }
  if (it.once) { return T("cfg.run.once_first"); }
  if ((it.port === "ain" || it.port === "aout") && it.channel > 0) { return T("cfg.run.points_first"); }
  if (peer === "human") { return T("cfg.run.ask_first"); }
  return T("cfg.run.timed", { d });
}

// The lines a configuration card adds below the item's settings: what a
// person gives, how it runs here, which sub-tests have it.
function cfgItemNotes(v, it) {
  const peer = cfgEffPeer(v, it), out = [];
  const mm = peer === "ref" && ((stageCfgs || {}).mismatch || {})[it.id];   // decision 134
  if (mm) { const d = el("div", "hint warn", mm); d.dataset.cfgmismatch = it.id; out.push(d); }
  if (cfgFixedRef(it) && peer === "human") {
    const d = el("div", "hint warn", T("cfg.no_auto")); d.dataset.cfgnoauto = it.id; out.push(d);
  } else {
    const h = cfgHumanText(it, peer);
    if (h) { const d = el("div", "hint", h); d.dataset.cfghuman = it.id; out.push(d); }
  }
  out.push(el("h3", null, T("cfg.run.head")));
  const r = el("div", null, cfgRunText(v, it, peer));
  r.dataset.cfgrun = it.id; out.push(r);
  const subs = FULL_SUBS.filter(s => s.k !== "safety" && it.stages.includes(s.k)).map(s => T(s.name));
  const i = el("div", "hint", T("cfg.run.in", { list: subs.join(T("shell.list_sep")) }));
  i.dataset.cfgin = it.id; out.push(i);
  return out;
}

// One item's description, criteria and notes for the AI / AO / DI / DO and
// relay cards of a configuration (cfgcard.js, relay.js).
function cfgItemInfo(v, it) {
  const box = el("div", "cfginfo"); box.dataset.cfginfo = it.id;
  box.appendChild(el("b", null, it.name));
  box.appendChild(el("div", null, it.what));
  for (const n of cfgItemNotes(v, it)) { box.appendChild(n); }
  box.appendChild(el("h3", null, T("full.crit")));
  const ib = ITEM_BODIES[it.id];
  if (it.id === "relay.cycle") { box.appendChild(el("div", "hint", T("cfg.crit.relay"))); }
  else if (it.id === "dout.load" && cfgEffPeer(v, it) === "ref") { box.appendChild(el("div", "hint", T("cfg.crit.dout_chain"))); }
  else { box.appendChild(ib ? el("div", "hint", ib.crit(it, v)) : libCritTable(it, null, true, cfgEffPeer(v, it))); }
  return box;
}
