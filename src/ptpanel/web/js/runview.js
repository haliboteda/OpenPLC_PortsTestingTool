"use strict";

/* ================= 工装完整测试, the right column =================
 *
 * 「正在进行」 (fixed height), the result list and the run log, all from
 * /api/full's run (src/ptpanel/journal.go). Every step has one number,
 * the same here, in the log and in the saved report (decision 128,
 * PORTTOOL-FLOW.md C.3.6 「正在进行、结果、日志」 and 「步号」).
 */
let runData = null;     // the last run object
let runId = 0;          // which run runLines belong to
let runLines = [];      // log lines kept on the page
let runSeq = 0;         // the last log line taken
let runLogNo = 0;       // the step the log is filtered to; 0 = all
let runHl = null;       // the step whose card is highlighted
let runSig = "";        // step states last drawn into the port list
let runDrawn = 0;       // log lines already in #runlog under the current filters

const runSince = () => runSeq;
const runStep = no => ((runData && runData.steps) || []).find(s => s.no === no);
const runSubOf = stage => FULL_SUBS.find(s => s.step === stage);

const RUN_VERDICT = { RUN: "run.v.run", PASS: "run.v.pass", FAIL: "run.v.fail", TIMEOUT: "run.v.timeout",
  ERROR: "run.v.error", STOPPED: "run.v.stopped", RECORDED: "run.v.recorded", SKIPPED: "run.v.skipped" };
const RUN_CLASS = { RUN: "run", PASS: "ok", RECORDED: "ok", FAIL: "bad", ERROR: "bad", TIMEOUT: "to" };
const RUN_BAD = new Set(["FAIL", "ERROR", "TIMEOUT"]);
const RUN_LV = { info: "run.lv.info", pass: "run.lv.pass", fail: "run.lv.fail", timeout: "run.lv.timeout", ask: "run.lv.ask" };

// Called on every /api/full: take the new log lines, redraw the column.
function runTake(j) {
  const r = j.run;
  if (!r) { runData = null; renderRunStat(); drawRunRes(); drawRunLog(true); return; }
  if (r.id !== runId) {
    runId = r.id; runLines = []; runSeq = 0; runLogNo = 0; runHl = null; runDrawn = 0;
  }
  for (const l of r.log || []) {
    if (l.seq > runSeq) { runLines.push(l); runSeq = l.seq; }
  }
  runData = r;
  renderRunStat();
  drawRunRes();
  drawRunLog(false);
  const sig = (r.steps || []).map(s => s.no + s.state).join(",");
  if (sig !== runSig) { runSig = sig; if (isFullSub(view)) { renderLports(); } }
}

/* ---------------- words for a step ---------------- */

const runPortLabel = port => ((catalog.ports.find(p => p.port === port) || {}).label) || port;
const runItemOf = id => catalog.items.find(it => it.id === id);

// 子测试 › 端口 › 测试项 › 通道, the parts there are.
function runWhere(st) {
  const sub = runSubOf(st.stage);
  const parts = [sub ? T(sub.name) : String(st.stage)];
  if (st.kind === "safety") { parts.push(T("run.where.safety")); }
  else if (st.kind === "whole") { parts.push(T("run.where.whole")); }
  else {
    parts.push(runPortLabel(st.port));
    const it = runItemOf(st.item);
    parts.push(it ? it.name : st.item);
    if (st.ch) { parts.push(chanName(st.port, st.ch)); }
  }
  return "#" + st.no + " " + parts.join(" › ");
}

const DIN_WHY = { ok: "lib.din.why.ok", wrong: "lib.din.why.wrong", none: "lib.din.why.none" };

// The step's latest readings in one line.
function runValue(st) {
  const out = [];
  if (st.drop !== null && st.drop !== undefined) { out.push(T("run.val.drop", { v: st.drop })); }
  else if (st.seen) { out.push(T("run.val.din", { v: st.seen })); }
  const ana = st.analog && (st.analog.rows || []).filter(r => r.got !== null && r.got !== undefined).pop();
  if (ana) {
    out.push(T("run.val.ana", { n: st.analog.rows.indexOf(ana) + 1, of: st.analog.rows.length, set: ana.set,
      got: ana.got, unit: st.analog.unit, dev: typeof ana.dev === "number" ? ana.dev.toFixed(2) : "-" }));
  }
  // Failed criteria first: they are why the step did not pass.
  const checks = (st.checks || []).slice().sort((x, y) => (x.pass ? 1 : 0) - (y.pass ? 1 : 0));
  for (const c of checks.slice(0, 3)) {
    out.push(checkField(st.port, c.check || {}) + " " + (c.missing ? T("ui.failCN.3") : c.absent ? T("ui.check.absent") : checkGot(c.check, c.got)));
  }
  if (!(st.checks || []).length) {
    for (const [k, v] of (st.values || []).slice(0, 3)) { out.push(fieldLabel(st.port, k) + " " + valueCN(v)); }
  }
  if (st.answer) {
    out.push(T("lib.res.answer", { a: st.answer === "yes" ? T("lib.res.yes") : st.answer === "no" ? T("lib.res.no") : st.answer }));
  }
  if (st.reason) { out.push(st.reason); }
  // A DI walk's channel ends with a code (ptlib/dinwalk.go), said in words.
  const din = st.item === "din.level" && st.ch && DIN_WHY[st.detail];
  if (din) { out.push(T(din)); }
  else if (st.detail && st.state !== "RUN") { out.push(st.detail); }
  if (st.why) { out.push(T("run.val.why", { why: st.why })); }
  return out.join(T("shell.list_sep")) || "…";
}

// The criterion of the picked peer, in one line.
function runCrit(st) {
  if (st.kind === "safety") { return T("run.crit.safety"); }
  const it = runItemOf(st.item);
  if (!it) { return "—"; }
  if (ITEM_BODIES[it.id]) { anaLoad(false); return ITEM_BODIES[it.id].crit(it, view); }  // full scale from /api/analog
  if (it.id === "relay.cycle") { return T("cfg.crit.relay"); }
  if (it.id === "dout.load" && st.peer === "ref") { return T("cfg.crit.dout_chain"); }
  const out = [];
  // A walked DI step is judged by its own channel's criterion (din.level lists DI1..DI8 in order).
  const crs = it.id === "din.level" && st.ch && (it.criteria || [])[st.ch - 1] ? [it.criteria[st.ch - 1]] : it.criteria || [];
  for (const cr of crs) {
    if (st.peer && cr.peers && !cr.peers.includes(st.peer)) { continue; }
    for (const c of cr.checks || []) { out.push(checkCN(it.port, c, cr.step)); }
  }
  return out.join(T("shell.list_sep")) || T("shell.item.crit_none");
}

const runSecs = ms => fmtSecs(Math.max(0, Math.round(ms / 1000)));

/* ---------------- 正在进行 ---------------- */

function renderRunStat() {
  const b = $("#runstat");
  if (!b) { return; }
  b.textContent = "";
  const r = runData;
  if (!r) { b.appendChild(el("div", "idle", T("run.now.idle"))); return; }
  const steps = r.steps || [];
  const running = steps.filter(s => s.state === "RUN");
  const cur = running.length ? running[running.length - 1] : steps[steps.length - 1];
  const t = r.tally || {};
  const kv = el("div", "kv");
  const row = (k, v) => {
    kv.appendChild(el("span", "k", T(k)));
    const x = el("span", "v", v); x.title = v; x.dataset.stat = k.split(".").pop();
    kv.appendChild(x);
  };
  if (r.ended || !cur) {
    b.appendChild(el("div", "big", T("run.now.sub")));
    b.appendChild(el("div", "now", T(r.ended ? "run.now.done" : "run.now.starting")));
    row("run.now.total", runSecs(r.now - r.started));
  } else {
    const sub = runSubOf(cur.stage);
    const stg = (r.stages || {})[cur.stage] || {};
    const dur = sub && sub.durs && stg.dur ? durLabel(stg.dur) : "";
    b.appendChild(el("div", "big", T("run.now.sub")));
    const now = el("div", "now", (sub ? T(sub.name) : "") + (dur ? T("run.now.dur", { dur }) : ""));
    now.title = now.textContent;
    b.appendChild(now);
    let item = cur.kind === "item" ? runWhere(cur).split(" › ").slice(2).join(" › ") : runWhere(cur).split(" › ").slice(1).join(" › ");
    if (running.length > 1) { item += T("run.now.more", { n: running.length - 1 }); }
    row("run.now.port", cur.port ? runPortLabel(cur.port) : "—");
    row("run.now.item", item);
    const it = cur.kind === "item" ? runItemOf(cur.item) : null;
    row("shell.item.what", it ? it.what : "—");   // decision 151
    row("run.now.peer", cur.peer ? T(PEER_NAME[cur.peer] || "lib.peer.none") : "—");
    row("run.now.step", "#" + cur.no + " / " + r.total);
    row("run.now.data", (cur.round ? T("lib.tm.round", { n: cur.round.no, verdict: libVerdict(cur.round.outcome) }) + T("shell.list_sep") : "") + runValue(cur));
    row("run.now.crit", runCrit(cur));
    row("run.now.item_t", runSecs(cur.dur_ms));
    row("run.now.sub_t", runSecs(r.now - (stg.started || r.now)) + (dur ? " / " + dur : ""));
    row("run.now.total", runSecs(r.now - r.started));
  }
  b.appendChild(kv);
  const pct = cur ? (((fullData || {}).pct || {})[cur.stage] || 0) : 0;
  const prog = el("div", "prog"), fill = el("i");
  fill.style.width = (r.ended ? 100 : Math.max(0, Math.min(100, pct))) + "%";
  prog.appendChild(fill); b.appendChild(prog);
  const tl = el("div", "tally");
  tl.appendChild(el("span", "ok", T("run.tally.pass", { n: t.pass || 0 })));
  tl.appendChild(el("span", "bad", T("run.tally.fail", { n: t.fail || 0 })));
  tl.appendChild(el("span", null, T("run.tally.timeout", { n: t.timeout || 0 })));
  tl.appendChild(el("span", null, T("run.tally.left", { n: t.left || 0 })));
  b.appendChild(tl);
}

/* ---------------- 结果 ---------------- */

function drawRunRes() {
  const b = $("#runres");
  if (!b) { return; }
  b.textContent = "";
  const steps = (runData && runData.steps) || [];
  if (!steps.length) { b.appendChild(el("div", "empty", T("run.res.empty"))); return; }
  const only = $("#resbad").checked;
  for (const st of steps) {
    if (only && !RUN_BAD.has(st.state)) { continue; }
    const d = el("div", "rr " + (RUN_CLASS[st.state] || ""));
    d.dataset.step = st.no;
    d.appendChild(el("span", "no", "#" + st.no));
    const wh = el("span", "wh", runWhere(st).replace(/^#\d+ /, "")); wh.title = wh.textContent;
    d.appendChild(wh);
    d.appendChild(el("span", "vr", T(RUN_VERDICT[st.state] || "run.v.error")));
    const vl = el("span", "vl", runValue(st) + " · " + T("run.res.took", { t: runSecs(st.dur_ms) }));
    vl.title = vl.textContent;
    d.appendChild(vl);
    d.onclick = () => runLocate(st.no);
    b.appendChild(d);
  }
}

/* ---------------- 日志 ---------------- */

function runStamp(ms) {
  const d = new Date(ms);
  return d.toTimeString().slice(0, 8) + "." + String(d.getMilliseconds()).padStart(3, "0");
}

function runLineShown(l) {
  if (l.kind === "raw" && !$("#lograw").checked) { return false; }
  if ($("#logbad").checked && l.lv !== "fail" && l.lv !== "timeout") { return false; }
  if (runLogNo && l.no !== runLogNo) { return false; }
  return true;
}

function runLineNode(l) {
  const d = el("div", "ln" + (l.kind === "raw" ? " raw" : "") + (l.kind === "sep" ? " sep" : ""));
  d.dataset.seq = l.seq;
  if (l.no) { d.dataset.no = l.no; }
  d.dataset.lv = l.lv;
  d.appendChild(el("span", "tm", runStamp(l.at)));
  const m = el("span", "msg");
  const st = l.no ? runStep(l.no) : null;
  if (st && l.kind !== "raw") {
    const a = el("span", "loc", runWhere(st)); a.onclick = () => runLocate(st.no); m.appendChild(a);
  }
  if (l.kind !== "raw" && l.kind !== "sep") { m.appendChild(el("span", "lv " + l.lv, T(RUN_LV[l.lv] || "run.lv.info"))); }
  let text = l.text || l.msg || "";
  if (l.kind === "start" && st) {
    const ps = Object.entries(st.params || {}).map(([k, v]) => paramCN(k) + " " + valueCN(String(v)));
    text = T("run.log.start") + (ps.length ? T("run.log.params", { list: ps.join(T("shell.list_sep")) }) : "");
  }
  // One line per round of a timed step (decision 151).
  if (l.kind === "round" && l.round) {
    const vals = libRoundVals(st ? st.port : "", l.round), args = { n: l.round.no, verdict: libVerdict(l.round.outcome), vals };
    text = T(vals ? "run.log.round_vals" : "run.log.round", args);
  }
  if (l.kind === "end" && st) {
    // The level tag already says pass / fail / timeout; other verdicts are said in words.
    const said = { PASS: 1, FAIL: 1, TIMEOUT: 1 }[st.state];
    text = (said ? "" : T(RUN_VERDICT[st.state] || "run.v.error") + " · ") + runValue(st);
  }
  m.appendChild(document.createTextNode(text));
  d.appendChild(m);
  return d;
}

// full redraws everything; otherwise only the lines not drawn yet are added.
function drawRunLog(full) {
  const b = $("#runlog");
  if (!b) { return; }
  const chip = $("#logchip"); chip.textContent = "";
  const st = runLogNo ? runStep(runLogNo) : null;
  if (st) {
    const c = el("span", "chip", T("run.log.only_step", { where: runWhere(st) }));
    c.title = T("run.log.chip_clear");
    c.onclick = () => { runLogNo = 0; drawRunLog(true); };
    chip.appendChild(c);
  }
  if (full) { b.textContent = ""; runDrawn = 0; }
  if (!runLines.length) {
    if (!b.firstChild) { b.appendChild(el("div", "empty", T("run.log.empty"))); }
    return;
  }
  const empty = b.querySelector(".empty");
  if (empty) { empty.remove(); }
  const atEnd = b.scrollTop + b.clientHeight >= b.scrollHeight - 20;
  for (const l of runLines.slice(runDrawn)) { if (runLineShown(l)) { b.appendChild(runLineNode(l)); } }
  runDrawn = runLines.length;
  if (atEnd || full) { b.scrollTop = b.scrollHeight; }
}

function runTab(t) {
  $("#tabres").classList.toggle("on", t === "res");
  $("#tablog").classList.toggle("on", t === "log");
  $("#paneres").hidden = t !== "res";
  $("#panelog").hidden = t !== "log";
}

/* ---------------- locating a step ---------------- */

// A result row or a log location: the middle shows that sub-test and port with
// the card marked, the log shows only that step.
function runLocate(no) {
  const st = runStep(no);
  if (!st) { return; }
  runHl = st;
  const sub = runSubOf(st.stage);
  if (sub && sub.k !== view) { pickView(sub.k); }
  if (st.port) { lport = st.port; }
  renderShell();
  runLogNo = no;
  runTab("log");
  drawRunLog(true);
}

// Called after layer 3 is drawn: mark the located step's card.
function runHighlight() {
  if (!runHl) { return; }
  const sub = runSubOf(runHl.stage);
  if (!sub || sub.k !== view || runHl.port !== lport || !runHl.item) { return; }
  const hit = document.querySelector('#itemcards [data-item="' + runHl.item + '"]');
  if (!hit) { return; }
  const card = hit.closest(".item") || hit;
  card.classList.add("hl");
  card.dataset.hl = runHl.item;
  card.scrollIntoView({ block: "nearest" });
}

// The port list's light for one sub-test: blue running, red not passed, green all passed.
function runPortDot(stage, port) {
  const d = el("span", "rdot");
  d.dataset.rdot = port;
  const steps = ((runData && runData.steps) || []).filter(s => s.stage === stage && s.port === port);
  if (!steps.length) { return d; }
  if (steps.some(s => s.state === "RUN")) { d.classList.add("run"); }
  else if (steps.some(s => RUN_BAD.has(s.state))) { d.classList.add("bad"); }
  else { d.classList.add("ok"); }
  return d;
}

$("#tabres").onclick = () => runTab("res");
$("#tablog").onclick = () => { runTab("log"); drawRunLog(true); };
$("#resbad").onchange = drawRunRes;
$("#logbad").onchange = () => drawRunLog(true);
$("#lograw").onchange = () => drawRunLog(true);
