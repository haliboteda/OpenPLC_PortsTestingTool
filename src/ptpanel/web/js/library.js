"use strict";

// The top-right Start and what comes back onto the item cards: /api/lib runs
// the ticked items, /api/ask asks the person. Judging is the server's (ptcheck,
// decision 77); this only draws it. PORTTOOL-FLOW.md C.3.6 "测试项怎么跑".

let libData = { running: false, states: {}, ports: {}, defaults: {}, ask: null };
let libEdits = {};      // `${item}` -> {param: value} typed on an R&D card
let libTimer = null;
let libAskShown = 0;    // id of the question on screen, so typing is not redrawn away

const libRunning = () => !!libData.running;
const libState = id => (libData.states || {})[id];

async function libRefresh() {
  const j = await api("/api/lib");
  if (!j || j.error) { return; }
  libData = j;
  libLogRounds();
  rdOnRefresh();
  libDrawAsk();
  if (!liveChoosing()) { liveKeepFocus(renderShell); }
  const busy = j.running || j.ask;
  if (busy && !libTimer) { libTimer = setInterval(libRefresh, 500); }
  if (!busy && libTimer) { clearInterval(libTimer); libTimer = null; }
}

// Which of the two card groups an item runs with (decision 115): "once" -
// checked once or asking a person - or "repeat", run for the duration. The AO
// load hold runs for the duration outside R&D (decision 114).
function itemKind(it, v) {
  if (it.load && v !== "rd") { return "repeat"; }
  // once_rd: once in R&D, repeated in the fixture tests (decision 151).
  return (it.once || (it.once_rd && v === "rd") || it.human || it.live || it.watch_s) ? "once" : "repeat";
}
const cardKind = (c, v) => c.items.some(it => itemKind(it, v) === "once") ? "once" : "repeat";

// The items Start sends: R&D the port on screen, a configuration every ticked port;
// calibration items are walked on the page instead (decision 97). kind limits it to one group.
function libPicks(kind) {
  const items = view === "rd" ? itemsOf(view, lport)
    : catalog.items.filter(it => it.stages.includes(view));
  // The other group's items are left out, so their last results stay on screen.
  return items.filter(it => !libPageItem(it) && (!kind || itemKind(it, view) === kind)).map(it => {
    const cfg = isCfgView(view);
    const params = cfg ? (cfgItem(view, it.id).params || {}) : (libEdits[it.id] || {});
    const peer = cfg ? cfgPeer(view, it) : (itemPeer[view + "|" + it.id] || defaultPeer(it));
    const pick = { id: it.id, on: itemOnIn(view, it), params, peer };
    const pts = cfg ? cfgItem(view, it.id).points : (ITEM_BODIES[it.id] ? anaPickPoints(it) : undefined);
    if (pts && pts.length) { pick.points = pts; }
    return pick;
  });
}

const libPageItem = it => !!(ITEM_BODIES[it.id] && ITEM_BODIES[it.id].run);
let libPageBusy = false;   // a calibration walk is running on the page
let libPageIds = [];       // which walks, so only their card's button turns to Stop

// R&D's ticked calibration walks first, one by one, then the rest on the
// server (PORTTOOL-FLOW.md C.3.6 "校准项").
async function libStart(kind) {
  if (memoBlocked()) { return; }
  await rdLeaving;   // the page left a moment ago may still be ending its session
  const dur = kind === "once" ? "once" : duration[view]; // R&D only: sub-tests start from the full test (decision 125)
  const walks = view === "rd" && kind !== "repeat"
    ? itemsOf(view, lport).filter(it => libPageItem(it) && itemOnIn(view, it)) : [];
  if (walks.length) {
    libPageBusy = true; renderShell();
    try {
      for (const it of walks) {
        if (await ITEM_BODIES[it.id].run(it, view) === null) { return; }
      }
    } finally { libPageBusy = false; renderShell(); }
  }
  const picks = libPicks(kind);
  if (walks.length && !picks.some(p => p.on)) { return; }
  const j = await api("/api/lib/start", { stage: view, duration: dur, items: picks });
  showMessage(j.error || "", !!j.error);
  libRefresh();
}

// R&D 「只测一次」: one card's own Start runs just its items (decision 116).
async function libStartOnly(ids) {
  if (memoBlocked()) { return; }
  await rdLeaving;
  const set = new Set(ids);
  const its = itemsOf(view, lport).filter(it => set.has(it.id));
  const walks = its.filter(libPageItem);
  if (walks.length) {
    libPageBusy = true; libPageIds = walks.map(it => it.id); renderShell();
    try {
      for (const it of walks) {
        if (await ITEM_BODIES[it.id].run(it, view) === null) { return; }
      }
    } finally { libPageBusy = false; libPageIds = []; renderShell(); }
  }
  if (!its.some(it => !libPageItem(it))) { return; }
  const picks = libPicks().filter(p => set.has(p.id)).map(p => Object.assign(p, { on: true }));
  const j = await api("/api/lib/start", { stage: view, duration: "once", items: picks });
  showMessage(j.error || "", !!j.error);
  libRefresh();
}

// The Start beside an R&D 「只测一次」 card's name: Stop while this card runs.
function libItemStart(ids) {
  const b = el("button", "primary", T("shell.item.start"));
  b.dataset.startitem = ids.join(",");
  const mine = ids.some(id => (libState(id) || {}).running) || (libPageBusy && ids.some(id => libPageIds.includes(id)));
  if (mine) { b.textContent = T("lib.stop"); b.className = "stop"; b.onclick = libStop; return b; }
  b.disabled = libRunning() || libPageBusy || !ids.length;
  b.onclick = () => libStartOnly(ids);
  return b;
}

async function libStop() {
  if (libPageBusy) { meterDone(null); return; }
  await api("/api/lib/stop", {});
  libRefresh();
}

// Called by renderRight for each group's Start: Start, or Stop while running.
function libStartButton(btn, ticked, kind) {
  if (libRunning() || libPageBusy) {
    btn.textContent = T("lib.stop");
    btn.className = "stop";
    btn.onclick = libStop;
    return;
  }
  btn.disabled = !ticked;
  btn.onclick = () => libStart(kind);
}

// Progress under the Start button.
function libRunNote() {
  const d = el("div", "why");
  if (libRunning()) {
    const t = fmtSecs(libData.elapsed_s || 0);
    d.textContent = libData.left_s === undefined ? T("lib.elapsed", { t })
      : T("lib.elapsed_left", { t, left: fmtSecs(libData.left_s) });
    if (libData.log) {
      const p = el("div", "mono", T("lib.runlog", { path: libData.log }));
      p.dataset.runlog = "1";
      d.appendChild(p);
    }
  } else if (libData.ended) {
    d.textContent = libData.ended;
  }
  return d;
}

/* ---- a timed R&D run: what, how long, which round (decision 151) ---- */

const libTimedRun = () => libRunning() && libData.stage === "rd" && !!libData.duration && libData.duration !== "once";
let libRoundSeen = {};   // item id -> the round already logged

// A round's key values in words.
const libRoundVals = (port, r) => (r.values || []).map(([k, v]) => fieldLabel(port, k) + " " + valueCN(v)).join(T("shell.list_sep"));
const libVerdict = o => T(LIB_OUTCOME[o] || "lib.o.error");

// One log line per finished round, not per frame (PORTTOOL-FLOW.md C.3.6 「时长测试看得懂」).
function libLogRounds() {
  for (const [id, st] of Object.entries(libData.states || {})) {
    const r = st.round;
    if (!r) { delete libRoundSeen[id]; continue; }
    if (libRoundSeen[id] === r.no || libData.stage !== "rd") { continue; }
    // A run that ended before this page saw it is not logged again.
    if (!libData.running && !(id in libRoundSeen)) { libRoundSeen[id] = r.no; continue; }
    libRoundSeen[id] = r.no;
    const it = catalog.items.find(x => x.id === id);
    if (!it) { continue; }
    const vals = libRoundVals(it.port, r);
    const args = { item: it.name, n: r.no, verdict: libVerdict(r.outcome), vals };
    addLog("panel", () => T(vals ? "lib.tm.log_vals" : "lib.tm.log", args));
  }
}

// The block at the top of a timed card: what it measures, time, rounds.
function libTimedNow(it, st) {
  const running = st.running && libTimedRun();
  if (!running && !st.round) { return null; }
  const box = el("div", "timednow"); box.dataset.timednow = it.id;
  if (running) {
    box.appendChild(el("div", null, T("lib.tm.what", { item: it.name, what: it.what })));
    const t = fmtSecs(libData.elapsed_s || 0);
    box.appendChild(el("div", null, libData.total_s ? T("lib.tm.time", { t, total: fmtSecs(libData.total_s) }) : T("lib.tm.time_open", { t })));
  }
  const r = st.round;
  if (!r) { box.appendChild(el("div", null, T("lib.tm.first"))); return box; }
  const verdict = libVerdict(r.outcome);
  const line = el("div", null, r.frames ? T("lib.tm.round_frames", { n: r.no, frames: r.frames, verdict }) : T("lib.tm.round", { n: r.no, verdict }));
  line.dataset.round = r.no;
  box.appendChild(line);
  const vals = libRoundVals(it.port, r);
  if (vals) { box.appendChild(el("div", "mono", T("lib.tm.vals", { vals }))); }
  return box;
}

function fmtSecs(s) {
  const h = Math.floor(s / 3600), m = Math.floor(s / 60) % 60, x = s % 60;
  return (h ? h + ":" : "") + String(m).padStart(h ? 2 : 1, "0") + ":" + String(x).padStart(2, "0");
}

const LIB_OUTCOME = { PASS: "lib.o.pass", FAIL: "lib.o.fail", TIMEOUT: "lib.o.timeout", ERROR: "lib.o.error",
  SKIPPED: "lib.o.skipped", STOPPED: "lib.o.stopped", RECORDED: "lib.o.recorded" };
const LIB_PILL = { PASS: "pass", FAIL: "fail", TIMEOUT: "fail", ERROR: "fail", SKIPPED: "skip",
  STOPPED: "skip", RECORDED: "rec" };

// Same names as ptlib.ChanName.
const chanName = (port, ch) => port === "temp" ? T("lib.chan.temp", { n: ch })
  : ({ din: "DI", dout: "DO", relay: "RY", aout: "AO", ain: "AI" }[port] || "") + ch;

function libPill(it) {
  if (ITEM_BODIES[it.id] && ITEM_BODIES[it.id].pill) { return ITEM_BODIES[it.id].pill(it); }
  const st = libState(it.id);
  if (!st) { return el("span", "pill", T("shell.item.notrun")); }
  if (st.running) { return el("span", "pill run", T("lib.o.running")); }
  // A multi-channel item has no result of its own, only one per channel (decision 129).
  if (st.chans && st.chans.length) {
    const box = el("span", "chpills"); box.dataset.chpills = it.id;
    for (const c of st.chans) {
      const name = it.port === "temp" ? chanName("temp", c.ch) : c.name;
      const p = el("span", "pill " + (LIB_PILL[c.outcome] || ""),
        name + " " + T(LIB_OUTCOME[c.outcome] || "lib.o.error") + (c.why ? " · " + c.why : ""));
      p.dataset.chan = c.name; p.dataset.outcome = c.outcome;
      box.appendChild(p);
    }
    return box;
  }
  return el("span", "pill " + (LIB_PILL[st.outcome] || ""), T(LIB_OUTCOME[st.outcome] || "lib.o.error"));
}

// R&D card parameters: the catalogue's editable ones, starting at the
// criteria step's value or the board's.
function libParamEditor(it) {
  const names = it.params || [];
  if (!names.length) { return el("div", null, T("shell.item.noparams")); }
  const box = el("div", "params");
  const def = (libData.defaults || {})[it.id] || {};
  for (const k of names) {
    box.appendChild(el("span", "pl", paramLabel(it, k)));
    const inp = paramField(it.port, k, (libEdits[it.id] || {})[k] ?? def[k] ?? "");
    inp.dataset.libparam = it.id + "." + k;
    inp.disabled = libRunning() && !rdOnlyCard();   // an R&D card's own session keeps its settings open
    const keep = x => {
      const e = libEdits[it.id] = libEdits[it.id] || {};
      if (x === "" || x === def[k]) { delete e[k]; } else { e[k] = x; }
    };
    const help = paramHelp(it.port, k);
    const cell = el("span");
    if (inp.tagName === "INPUT" && view === "rd") {
      // Typed: checked and kept only when good (memo.js, C.3.6 「填数超量程」).
      cell.appendChild(memoCell(inp, "par|" + it.id + "|" + k, () => (libEdits[it.id] || {})[k] ?? def[k] ?? "", keep));
    } else {
      inp.onchange = () => keep(inp.value);
      cell.appendChild(inp);
    }
    if (help) { inp.title = help; }
    box.appendChild(cell);
  }
  return box;
}

// A parameter the board states as a list (pt.caps limits=) is picked from it,
// anything else typed. blank, when given, is the value an empty choice leaves.
function paramField(port, k, value, blank) {
  const p = (state.ports || []).find(x => x.name === port);
  const en = p && p.limits && p.limits[k] && p.limits[k].enum;
  if (!en || !en.length) {
    const i = el("input"); i.value = value;
    if (blank !== undefined) { i.placeholder = blank; }
    return i;
  }
  const s = el("select");
  if (blank !== undefined) { s.appendChild(new Option(T("param.default_opt", { v: blank }), "")); }
  const note = (PARAM_VALUE_NOTE[port] || {})[k] || {};
  for (const v of en) { s.appendChild(new Option(note[v] || valueCN(v), v)); }
  if (value !== "" && !en.includes(String(value))) { s.appendChild(new Option(String(value), String(value))); }
  s.value = String(value);
  return s;
}

// A criterion without its field name: the 预期 column (decision 113).
function expectCN(port, c) { return checkCN(port, Object.assign({}, c, { field: "" })).replace("", "").trim(); }

// 项目 / 预期 / 结果, one row per criterion; 结果 stays empty until the item has
// run. The board's other reported fields follow with an empty 预期 (decision 116).
// plain drops 结果: a sub-test's card shows no measured values (decision 125).
// peer, when given, keeps only the criteria that peer runs (a configuration card).
function libCritTable(it, st, plain, peer) {
  const rows = [], judged = new Set();
  const done = (st && st.checks) || [];
  for (const cr of it.criteria || []) {
    if (peer && cr.peers && !cr.peers.includes(peer)) { continue; }
    if (cr.missing) { rows.push([T("shell.item.crit_missing", { step: cr.step }), "", null]); continue; }
    for (const ch of cr.checks || []) {
      const r = done.find(x => x.check && x.check.field === ch.field && x.check.op === ch.op && !x.used);
      if (r) { r.used = true; }
      judged.add(ch.field);
      rows.push([checkField(it.port, ch, cr.step), expectCN(it.port, ch), r || null]);
    }
  }
  // Checks the run reported that the catalogue did not list (a plan step changed).
  for (const r of done) {
    if (!r.used) { judged.add((r.check || {}).field); rows.push([checkField(it.port, r.check || {}), expectCN(it.port, r.check || {}), r]); }
  }
  for (const r of done) { delete r.used; }
  if (st && !st.live) {
    for (const [k, v] of st.values || []) { if (!judged.has(k)) { rows.push([fieldLabel(it.port, k), "", { raw: v }]); } }
  }
  if (!rows.length) { return el("div", "hint", it.live || it.watch_s ? "" : T("shell.item.crit_none")); }
  const t = el("table", "crit");
  const hr = el("tr");
  for (const k of ["shell.item.col.item", "shell.item.col.want", "shell.item.col.got"].slice(0, plain ? 2 : 3)) { hr.appendChild(el("th", null, T(k))); }
  t.appendChild(hr);
  for (const [name, want, r] of rows) {
    const tr = el("tr", r && r.raw === undefined ? (r.pass ? "ok" : "bad") : "");
    tr.appendChild(el("td", null, name));
    tr.appendChild(el("td", null, want));
    if (plain) { t.appendChild(tr); continue; }
    const got = !r ? "" : r.raw !== undefined ? valueCN(r.raw) : r.missing ? "❌ " + T("ui.failCN.3")
      : r.absent ? "✅ " + T("ui.check.absent")
      : (r.pass ? "✅ " : "❌ ") + (r.got === "" ? T("ui.failCN.3") : checkGot(r.check, r.got));
    tr.appendChild(el("td", "mono", got));
    t.appendChild(tr);
  }
  return t;
}

// The right half of a card: the criteria table, then what went wrong.
function libResult(it) {
  const st = libState(it.id);
  const box = el("div");
  const now = st && libTimedNow(it, st);
  if (now) { box.appendChild(now); }
  // Only the picked peer's criteria, as on a configuration card.
  box.appendChild(libCritTable(it, st, false, itemPeer["rd|" + it.id] || defaultPeer(it)));
  if (!st) { box.appendChild(el("div", "hint", T("shell.item.empty"))); return box; }
  if (st.running) { box.appendChild(el("div", "hint", T("lib.res.running"))); }
  if (st.answer) {
    const a = st.answer === "yes" ? T("lib.res.yes") : st.answer === "no" ? T("lib.res.no") : st.answer;
    box.appendChild(el("div", null, T("lib.res.answer", { a })));
  }
  if (st.live) { box.appendChild(libLive(it, st)); }
  if (st.drop) { box.appendChild(libDrop(st.drop)); }
  if (st.din_walk) { box.appendChild(libDinWalk(st.din_walk)); }
  // On loop=ctrl ports the counts ride the control port, not this one (decision 9).
  const p = (state.ports || []).find(x => x.name === it.port);
  if (p && p.loop === "ctrl" && !st.live && (st.values || []).some(([k]) => k === "seq" || k === "rx" || k === "miss")) {
    box.appendChild(el("div", "hint", T("lib.res.ctrl_counts"))).dataset.ctrlcounts = it.id;
  }
  // Below the table: each failed criterion said as a sentence.
  for (const r of st.checks || []) {
    if (!r.pass) { box.appendChild(el("div", "chk bad", "❌ " + failCN(it.port, r))); }
  }
  // A watched session's event frames, as the board wrote them (decision 105).
  if ((st.events || []).length) {
    box.appendChild(el("div", "sub", T("lib.res.events", { n: st.events.length })));
    const pre = el("pre", "mono events", st.events.join("\n"));
    pre.dataset.events = it.id;
    box.appendChild(pre);
  }
  if (st.rounds > 1 && !st.round) { box.appendChild(el("div", "hint", T("lib.res.rounds", { n: st.rounds }))); }
  // A multi-channel item's reasons are on its channels' pills (decision 129).
  if (st.reason && !(st.chans && st.chans.length)) { box.appendChild(el("div", "hint", st.reason)); }
  if (st.detail) { box.appendChild(el("div", "hint mono", T("lib.res.detail", { detail: st.detail }))); }
  if (st.outcome === "RECORDED") { box.appendChild(el("div", "hint", T("lib.res.recorded"))); }
  return box;
}

// Port list: the port's verdict from the last run of its items.
function libPortBadge(port) {
  const v = (libData.ports || {})[port];
  if (!v) { return el("span"); }
  const b = el("span", "pv " + v, T({ pass: "lib.pv.pass", fail: "lib.pv.fail", unjudged: "lib.pv.unjudged" }[v] || "lib.pv.unjudged"));
  b.dataset.portverdict = v;
  return b;
}

// What the board reads now, under a question the board answers.
function libAskNow(box, q) {
  const now = box.querySelector('[data-ask="now"]'), dw = (libState(q.item) || {}).din_walk;
  if (now) { now.textContent = dw && dw.now ? T("lib.ask.din_now", { v: dinBitsText(dw.now) }) : ""; }
}

// The question box lives outside the cards: they are redrawn every poll and
// would wipe what is being typed (same reason as #meterask in index.html).
function libDrawAsk() {
  let box = $("#libask");
  const q = libData.ask;
  if (!q) { if (box) { box.hidden = true; } libAskShown = 0; return; }
  if (q.id === libAskShown && box && !box.hidden) { libAskNow(box, q); return; }
  libAskShown = q.id;
  if (!box) { box = el("div"); box.id = "libask"; box.className = "handbox"; document.body.appendChild(box); }
  box.hidden = false;
  box.textContent = "";
  box.appendChild(el("b", null, "🍍 " + q.text));
  if (q.hint) { box.appendChild(el("div", "hint", q.hint)); }
  if (q.prompt) { box.appendChild(el("div", "hint", T("lib.ask.plan_prompt", { text: q.prompt }))); }
  const row = el("div", "row");
  const send = async body => {
    const j = await api("/api/ask", Object.assign({ id: q.id }, body));
    if (j.error) { showMessage(j.error, true); return; }
    libAskShown = 0; box.hidden = true; libRefresh();
  };
  if (q.kind === "wait") {
    // Nothing to type: the board's reading ends the question (decision 128).
    const now = el("div", "mono"); now.dataset.ask = "now"; box.appendChild(now); libAskNow(box, q);
  } else if (q.kind === "yes_no") {
    const y = el("button", "primary", T("lib.ask.yes")); y.dataset.ask = "yes"; y.onclick = () => send({ yes: true });
    const n = el("button", null, T("lib.ask.no")); n.dataset.ask = "no"; n.onclick = () => send({ yes: false });
    row.appendChild(y); row.appendChild(n);
  } else {
    const v = el("input"); v.type = "number"; v.step = "any"; v.dataset.ask = "value";
    row.appendChild(v);
    if (q.unit) { row.appendChild(el("span", "hint", q.unit)); }
    const ok = el("button", "primary", T("lib.ask.ok")); ok.dataset.ask = "ok";
    ok.onclick = () => send({ value: v.value });
    row.appendChild(ok);
  }
  const stop = el("button", null, T("lib.stop")); stop.dataset.ask = "stop"; stop.onclick = () => send({ stop: true });
  row.appendChild(stop);
  box.appendChild(row);
}
