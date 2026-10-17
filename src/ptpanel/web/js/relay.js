"use strict";

// The relay card (decision 123, PORTTOOL-FLOW.md C.3.6 「继电器开合」): one row
// per relay with its own holds and count, 按间隔 or 按时长, parallel or serial.
// R&D edits it any time and starts it here; the configurations keep only its
// parameters and the full test runs them. Values live in the item's params as
// relay:value pairs (ptlib/relay.go); 按时长 is turned into close / open ms here.

const RELAY_ID = "relay.cycle";
const RELAY_N = 6;
const RELAY_MIN_MS = 1000;      // the board's floor (PORTTOOL-FLOW.md B.2)
const RELAY_WARN_ABOVE = 100;   // decision 123: red hint, never a block
let relayText = {};             // "<view>|<ch>|<field>" -> what is typed, as typed
let relayStartErr = "";

const relayItemIn = v => catalog.items.find(it => it.id === RELAY_ID && it.stages.includes(v));
const relayParams = v => (isCfgView(v) ? cfgItem(v, RELAY_ID).params : libEdits[RELAY_ID]) || {};

// "2000" (every relay) or "1:2000,2:1500" -> {1: "2000", ...}
function relayPairs(s) {
  const out = {};
  if (!s) { return out; }
  if (!String(s).includes(":")) { for (let c = 1; c <= RELAY_N; c++) { out[c] = String(s); } return out; }
  for (const part of String(s).split(",")) { const [c, x] = part.split(":"); if (c && x !== undefined) { out[c] = x; } }
  return out;
}
function relayJoin(m) {
  return Object.keys(m).map(Number).sort((a, b) => a - b).filter(c => m[c] !== "" && m[c] !== undefined)
    .map(c => c + ":" + m[c]).join(",");
}

// A sub-test with a duration runs its relays by that duration: only the count
// is typed (decision 123; ptlib.RelayForDuration works out the holds again at run time).
const relayTimed = v => isCfgView(v) && cfgDuration(v) !== "once";
const relayBy = v => relayTimed(v) ? "time" : (relayParams(v).by || "gap");
const relayNum = v => (String(v ?? "").trim() === "" ? NaN : Number(v));

// The minutes one relay's count takes in a timed sub-test: the whole duration,
// or an even share of it when the relays run one after another.
function relayTestMinutes(v, P) {
  const n = (P.ch || "").split(",").filter(x => x).length;
  return String(cfgSeconds(v) / 60 / ((P.order || "par") === "ser" && n ? n : 1));
}

// One relay's row as the card shows it: holds in s, count, minutes.
function relayRow(v, ch, P = relayParams(v)) {
  const get = (k, f, field = k) => {
    const t = relayText[v + "|" + ch + "|" + field];
    if (t !== undefined) { return t; }
    const x = relayPairs(P[k])[ch];
    return x === undefined ? "" : f ? f(x) : x;
  };
  const ms = x => String(Number(x) / 1000);
  return {
    ch, on: (P.ch || "").split(",").includes(String(ch)),
    closeS: get("close", ms, "closeS"), openS: get("open", ms, "openS"), count: get("count"),
    minutes: relayTimed(v) ? relayTestMinutes(v, P) : get("minutes"),
  };
}

// Holds in ms, the count, the total time in s and what is wrong with the row.
function relayPlan(r, by) {
  const n = relayNum(r.count);
  let close, open;
  if (by === "gap") {
    close = Math.round(relayNum(r.closeS) * 1000); open = Math.round(relayNum(r.openS) * 1000);
  } else {
    // 按时长: each cycle is time ÷ count, closed and open half each (decision 123).
    const half = Math.round(relayNum(r.minutes) * 60000 / n / 2);
    close = half; open = half;
  }
  const missing = (by === "gap" ? [r.closeS, r.openS, r.count] : [r.minutes, r.count])
    .some(x => !isFinite(relayNum(x))) || !(Number.isInteger(n) && n >= 1);
  const tooFast = !missing && !(close >= RELAY_MIN_MS && open >= RELAY_MIN_MS);
  return { n, close, open, total: n * (close + open) / 1000, missing, tooFast, over: n > RELAY_WARN_ABOVE };
}

// Writes the card's state into the item's params: typed values, and the holds
// the board takes (worked out here for 按时长, rounded to ms).
function relayStore(v, change) {
  const apply = P => {
    change(P);
    const keep = { close: {}, open: {}, count: {}, minutes: {} };
    for (let c = 1; c <= RELAY_N; c++) {
      const r = relayRow(v, c, P), p = relayPlan(r, P.by || "gap");
      if (isFinite(p.close) && p.close > 0) { keep.close[c] = p.close; }
      if (isFinite(p.open) && p.open > 0) { keep.open[c] = p.open; }
      // Only what ptlib.RelayHostParamOK and the board accept is kept.
      const x = { count: relayNum(r.count), minutes: relayNum(r.minutes) };
      if (Number.isInteger(x.count) && x.count >= 1) { keep.count[c] = String(x.count); }
      if (x.minutes > 0 && !relayTimed(v)) { keep.minutes[c] = String(x.minutes); }
    }
    for (const k of Object.keys(keep)) { const s = relayJoin(keep[k]); if (s) { P[k] = s; } else { delete P[k]; } }
    delete P.ratio;   // retired (decision 123); old saved cards may still carry it
  };
  if (isCfgView(v)) {
    cfgChange(v, c => { const ic = c.items[RELAY_ID] = c.items[RELAY_ID] || {}; ic.params = ic.params || {}; apply(ic.params); });
  } else {
    apply(libEdits[RELAY_ID] = libEdits[RELAY_ID] || {});
  }
}

// Typed text of one view's cells: dropped when 按间隔 / 按时长 changes, since
// the holds then come from the other columns.
function relayForget(v) { for (const k of Object.keys(relayText)) { if (k.startsWith(v + "|")) { delete relayText[k]; } } }

function relaySetKey(v, k, val) { relayStore(v, P => { if (val === "") { delete P[k]; } else { P[k] = val; } }); }

function relayTick(v, ch, on) {
  relayStore(v, P => {
    const s = new Set((P.ch || "").split(",").filter(x => x));
    if (on) { s.add(String(ch)); } else { s.delete(String(ch)); }
    const list = [...s].map(Number).sort((a, b) => a - b).join(",");
    if (list) { P.ch = list; } else { delete P.ch; }
  });
}

// The header's select-all: half-ticked when only some relays are (decision 151 item 10).
function relayAllBox(v, locked) {
  const th = el("th");
  const n = Array.from({ length: RELAY_N }, (_, i) => relayRow(v, i + 1)).filter(r => r.on).length;
  const all = el("input"); all.type = "checkbox"; all.dataset.relayall = "1"; all.disabled = locked;
  all.title = T("relay.all");
  all.checked = n === RELAY_N; all.indeterminate = n > 0 && n < RELAY_N;
  all.onchange = () => {
    relayStore(v, P => {
      if (all.checked) { P.ch = Array.from({ length: RELAY_N }, (_, i) => i + 1).join(","); } else { delete P.ch; }
    });
    if (!isCfgView(v)) { renderRight(); }
  };
  th.appendChild(all);
  return th;
}

function relayFmt(s) {
  if (!isFinite(s)) { return "—"; }
  s = Math.round(s);
  const h = Math.floor(s / 3600), m = Math.floor(s % 3600 / 60), x = s % 60;
  return (h ? T("relay.t.h", { n: h }) + " " : "") + (h || m ? T("relay.t.m", { n: m }) + " " : "") + T("relay.t.s", { n: x });
}

function relaySeg(name, cur, opts, onpick, disabled) {
  const seg = el("span", "seg"); seg.dataset.relayseg = name;
  for (const [val, key] of opts) {
    const lb = el("label", cur === val ? "cur" : "");
    const i = el("input"); i.type = "radio"; i.name = "relay-" + name; i.value = val; i.checked = cur === val; i.disabled = disabled;
    i.onchange = () => onpick(val);
    lb.appendChild(i); lb.appendChild(document.createTextNode(T(key)));
    seg.appendChild(lb);
  }
  return seg;
}

// One cycle of the first ticked relay, so people see which hold is which.
function relayWave(v) {
  const r = Array.from({ length: RELAY_N }, (_, i) => relayRow(v, i + 1)).find(x => x.on);
  const p = r && relayPlan(r, relayBy(v));
  const ok = p && !p.missing && p.close > 0 && p.open > 0;
  const c = ok ? p.close / 1000 : 1, o = ok ? p.open / 1000 : 1, W = 520, k = W / 2.5 / (c + o);
  const xs = [0, o * k, (o + c) * k, (2 * o + c) * k, (2 * o + 2 * c) * k].map(x => 70 + x);
  const hi = 12, lo = 40, NS = "http://www.w3.org/2000/svg";
  const svg = document.createElementNS(NS, "svg");
  svg.setAttribute("width", W + 140); svg.setAttribute("height", 62);
  const text = (x, y, t, mid) => {
    const n = document.createElementNS(NS, "text"); n.setAttribute("x", x); n.setAttribute("y", y);
    if (mid) { n.setAttribute("text-anchor", "middle"); }
    n.textContent = t; svg.appendChild(n);
  };
  text(0, hi + 4, T("relay.wave.closed")); text(0, lo + 4, T("relay.wave.open"));
  const path = document.createElementNS(NS, "path");
  path.setAttribute("d", `M${xs[0]},${lo} H${xs[1]} V${hi} H${xs[2]} V${lo} H${xs[3]} V${hi} H${Math.min(xs[4], 70 + W)}`);
  path.setAttribute("fill", "none"); path.setAttribute("stroke", "var(--accent)"); path.setAttribute("stroke-width", "2");
  svg.appendChild(path);
  const secs = x => ok ? " " + T("relay.secs", { n: x.toFixed(1) }) : "";
  text((xs[0] + xs[1]) / 2, 56, T("relay.wave.open_hold") + secs(o), true);
  text((xs[1] + xs[2]) / 2, 8, T("relay.wave.close_hold") + secs(c), true);
  text((xs[2] + xs[3]) / 2, 56, T("relay.wave.one"), true);
  const box = el("div", "rlwave");
  box.appendChild(svg);
  box.appendChild(el("div", "hint", r ? T("relay.wave.note", { n: r.ch }) : ""));
  return box;
}

// Why Start would be refused; "" when it can start.
function relayWhyNot(v) {
  const on = Array.from({ length: RELAY_N }, (_, i) => relayRow(v, i + 1)).filter(r => r.on).map(r => relayPlan(r, relayBy(v)));
  if (!on.length) { return T("relay.err.none"); }
  if (on.some(p => p.missing)) { return T("relay.err.missing"); }
  if (on.some(p => p.tooFast)) { return T("relay.err.fast"); }
  return "";
}

// The ticked relays' total time in s: the longest when together, the sum one
// after another; NaN while a value is missing.
function relayTotal(v) {
  const on = Array.from({ length: RELAY_N }, (_, i) => relayRow(v, i + 1)).filter(r => r.on).map(r => relayPlan(r, relayBy(v)));
  if (!on.length || on.some(p => p.missing)) { return NaN; }
  return (relayParams(v).order || "par") === "par" ? Math.max(...on.map(p => p.total)) : on.reduce((a, p) => a + p.total, 0);
}

function relaySummary(v) {
  const order = relayParams(v).order || "par";
  const on = Array.from({ length: RELAY_N }, (_, i) => relayRow(v, i + 1)).filter(r => r.on);
  if (!on.length) { return T("relay.sum.none"); }
  const t = relayTotal(v);
  if (!isFinite(t)) { return T("relay.sum.missing"); }
  return T("relay.sum", { n: on.length, order: T(order === "par" ? "relay.order.par" : "relay.order.ser"), t: relayFmt(t) });
}

// Redraws the parts that follow typed values, keeping focus in the cell.
function relayRefresh(card, v) {
  for (let c = 1; c <= RELAY_N; c++) {
    const r = relayRow(v, c), p = relayPlan(r, relayBy(v));
    const w = card.querySelector(`[data-relaywarn="${c}"]`);
    if (w) { w.hidden = !p.over; }
    const f = card.querySelector(`[data-relayfast="${c}"]`);
    if (f) { f.hidden = !p.tooFast; }
    const d = card.querySelector(`[data-relayderived="${c}"]`);
    if (d) {
      d.textContent = p.missing ? "—" : relayBy(v) === "gap" ? relayFmt(p.total)
        : T("relay.derived", { c: (p.close / 1000).toFixed(1), o: (p.open / 1000).toFixed(1) });
    }
    const of = card.querySelector(`[data-relayof="${c}"]`);
    if (of) { of.textContent = " / " + (isFinite(p.n) ? p.n : "—"); }
  }
  card.querySelector("[data-relaysum]").textContent = relaySummary(v);
  const wave = card.querySelector(".rlwave");
  if (wave) { wave.replaceWith(relayWave(v)); }
}

function relayInput(card, v, r, k, step, locked) {
  const i = el("input"); i.type = "number"; i.min = "0"; i.step = step;
  i.value = r[k]; i.placeholder = T("relay.required"); i.disabled = locked;
  i.dataset.relay = r.ch + "|" + k;
  i.oninput = () => {
    relayText[v + "|" + r.ch + "|" + k] = i.value;
    if (!isCfgView(v)) { relayStore(v, () => {}); }
    relayRefresh(card, v);
  };
  // Configurations save on leaving the cell: saving redraws the card.
  if (isCfgView(v)) { i.onchange = () => relayStore(v, () => {}); }
  return i;
}

function relayCard(port) {
  const v = view, it = relayItemIn(v);
  if (!it) { return el("div"); }
  const cfg = isCfgView(v), locked = libRunning();
  const P = relayParams(v), by = relayBy(v), order = P.order || "par", timed = relayTimed(v);
  const st = libState(RELAY_ID) || {};
  const live = {};
  for (const c of (st.relay || {}).chans || []) { live[c.ch] = c; }

  const card = el("div", "item relaycard" + (cfg && !itemOnIn(v, it) ? " off" : ""));
  card.dataset.item = RELAY_ID;
  const hd = el("div", "ih");
  if (cfg) {
    const c = el("input"); c.type = "checkbox"; c.checked = itemOnIn(v, it); c.dataset.pick = RELAY_ID;
    c.disabled = locked; c.onchange = () => cfgTick(v, it, c.checked);
    hd.appendChild(c);
  }
  hd.appendChild(el("b", null, it.name));
  hd.appendChild(el("span", "hint", T("relay.card.hint")));
  hd.appendChild(el("span", "sp"));
  // Who reports the load's cycles: a person in R&D, the reference DI in the fixture tests (decisions 130, 133).
  if (cfg) {
    hd.appendChild(el("span", "hint", T("cfg.src_fixed")));
  } else {
    hd.appendChild(libPill(it)); // a sub-test's card shows no result (decision 125)
  }
  card.appendChild(hd);

  const body = el("div", "rlbody");
  const opts = el("div", "rlopts");
  opts.appendChild(el("span", "lb", T("relay.by")));
  const o1 = el("span");
  if (timed) {
    o1.appendChild(el("span", "hint", T("relay.by.test", { d: durLabel(cfgDuration(v)) }))).dataset.relaybytest = "1";
  } else {
    o1.appendChild(relaySeg("by", by, [["gap", "relay.by.gap"], ["time", "relay.by.time"]], x => { relayForget(v); relaySetKey(v, "by", x); renderRight(); }, locked));
    o1.appendChild(el("span", "hint", T(by === "gap" ? "relay.by.gap_d" : "relay.by.time_d")));
  }
  opts.appendChild(o1);
  opts.appendChild(el("span", "lb", T("relay.order")));
  const o2 = el("span");
  o2.appendChild(relaySeg("order", order, [["par", "relay.order.par"], ["ser", "relay.order.ser"]], x => { relaySetKey(v, "order", x); renderRight(); }, locked));
  o2.appendChild(el("span", "hint", T(order === "par" ? "relay.order.par_d" : "relay.order.ser_d")));
  opts.appendChild(o2);
  body.appendChild(opts);
  body.appendChild(relayWave(v));

  const tbl = el("table", "gtab rltab"), hr = el("tr");
  const cols = by === "gap"
    ? ["", "relay.col.relay", "relay.col.close", "relay.col.open", "relay.col.count", "relay.col.est", "relay.col.run"]
    : ["", "relay.col.relay", "relay.col.minutes", "relay.col.count", "relay.col.derived", "relay.col.run"];
  if (timed) { cols.splice(2, 1); } // the minutes are the test's own
  if (cfg) { cols.pop(); } // no run column in a sub-test (decision 125)
  for (const k of cols) { hr.appendChild(k ? el("th", null, T(k)) : relayAllBox(v, locked)); }
  tbl.appendChild(hr);
  for (let c = 1; c <= RELAY_N; c++) {
    const r = relayRow(v, c);
    const tr = el("tr", r.on ? "" : "off"); tr.dataset.relayrow = c;
    const tick = el("input"); tick.type = "checkbox"; tick.checked = r.on; tick.disabled = locked;
    tick.dataset.relaytick = c;
    tick.onchange = () => { relayTick(v, c, tick.checked); if (!cfg) { renderRight(); } };
    tr.appendChild(el("td")).appendChild(tick);
    tr.appendChild(el("td", null, T("relay.row", { n: c })));
    const cell = (k, step, unit) => {
      const td = el("td");
      td.appendChild(relayInput(card, v, r, k, step, locked));
      if (unit) { td.appendChild(el("span", "unit", T(unit))); }
      return td;
    };
    const countTd = cell("count", "1");
    const warn = el("span", "rlwarn", T("relay.warn100")); warn.dataset.relaywarn = c;
    countTd.appendChild(warn);
    const fast = el("div", "rlerr", T("relay.too_fast")); fast.dataset.relayfast = c;
    const derived = el("td", "mono"); derived.dataset.relayderived = c;
    if (by === "gap") {
      tr.appendChild(cell("closeS", "0.1", "relay.unit.s"));
      const openTd = cell("openS", "0.1", "relay.unit.s"); openTd.appendChild(fast); tr.appendChild(openTd);
      tr.appendChild(countTd);
      tr.appendChild(derived);
    } else {
      if (!timed) { tr.appendChild(cell("minutes", "1", "relay.unit.min")); }
      tr.appendChild(countTd);
      derived.appendChild(fast); tr.appendChild(derived);
    }
    // While it runs, and after: k / N and whether it is closed now.
    const lc = live[c] || {};
    const run = el("td", "rlrun");
    run.appendChild(el("span", "rllamp" + (lc.closed ? " on" : "")));
    const cnt = el("span", "mono rlcnt", String(lc.n || 0)); cnt.dataset.relaycnt = c;
    run.appendChild(cnt);
    const of = el("span", "hint"); of.dataset.relayof = c; run.appendChild(of);
    const bar = el("span", "rlbar"), fill = el("i");
    const n = lc.count || relayPlan(r, relayBy(v)).n;
    fill.style.width = (n > 0 ? Math.min(100, (lc.n || 0) * 100 / n) : 0) + "%";
    bar.appendChild(fill); run.appendChild(bar);
    const phase = lc.phase === "run" ? (lc.closed ? "relay.state.closed" : "relay.state.open")
      : { queue: "relay.state.queue", done: "relay.state.done", stopped: "relay.state.stopped" }[lc.phase];
    const ph = el("span", "hint", phase ? T(phase) : ""); ph.dataset.relayphase = c; run.appendChild(ph);
    if (!cfg) { tr.appendChild(run); }
    tbl.appendChild(tr);
  }
  body.appendChild(tbl);

  if (!cfg) { body.appendChild(el("div", "hint", T("relay.crit.rd"))); }   // R&D's criterion: the person counts (decision 133)
  const foot = el("div", "rlfoot");
  const copy = el("button", null, T("relay.copy")); copy.dataset.relaycopy = "1"; copy.disabled = locked;
  copy.onclick = () => {
    const on = Array.from({ length: RELAY_N }, (_, i) => relayRow(v, i + 1)).filter(r => r.on);
    if (on.length < 2) { return; }
    for (const r of on.slice(1)) {
      for (const k of ["closeS", "openS", "count", "minutes"]) { relayText[v + "|" + r.ch + "|" + k] = on[0][k]; }
    }
    relayStore(v, () => {});
    renderRight();
  };
  foot.appendChild(copy);
  const sum = el("span", "hint"); sum.dataset.relaysum = "1"; foot.appendChild(sum);
  foot.appendChild(el("span", "sp"));
  if (!cfg) {
    const start = libItemStart([RELAY_ID]);
    if (!start.classList.contains("stop")) {
      start.onclick = () => {
        relayStartErr = relayWhyNot(v);
        if (relayStartErr) { renderRight(); return; }
        libStartOnly([RELAY_ID]);
      };
    }
    foot.appendChild(start);
  }
  body.appendChild(foot);
  const err = el("div", "rlerr"); err.dataset.relayerr = "1";
  err.textContent = cfg ? "" : relayStartErr;
  if (!cfg && !relayWhyNot(v)) { relayStartErr = ""; err.textContent = ""; }
  body.appendChild(err);

  if (st.answer && !cfg) {
    const a = st.answer === "yes" ? T("lib.res.yes") : st.answer === "no" ? T("lib.res.no") : st.answer;
    body.appendChild(el("div", null, T("lib.res.answer", { a })));
  }
  if (st.reason && !cfg) { body.appendChild(el("div", "hint", st.reason)); }
  if (st.detail && !cfg) { body.appendChild(el("div", "hint mono", T("lib.res.detail", { detail: st.detail }))); }
  card.appendChild(body);
  if (cfg) { card.appendChild(cfgItemInfo(v, it)); } // what, criterion, how it runs here (C.3.6 layer 3)
  relayRefresh(card, v);
  return card;
}
