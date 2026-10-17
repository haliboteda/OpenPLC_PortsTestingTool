"use strict";

// AI / AO item bodies for the generic card (ITEM_BODIES[id]): range, test points,
// per-point results and the calibration walk (decisions 97, 98, 99, 102; PORTTOOL-FLOW.md C.3.6).
// Only the calibration walk runs here; fits and verdicts are the server's (ptlib/analog.go).

const ITEM_BODIES = {};

let anaState = null;    // /api/analog for the connected board
let anaKey = "";        // which connection anaState belongs to
let anaLoading = false;
let anaRes = {};        // calibration item id -> {rows, fit, points, note, bad, stopped}
let anaPtsText = {};    // R&D: item id -> points as the person typed them
let anaBtn = {};        // "store" / "burn" -> {busy, note, ok}
let anaWalking = "";    // the calibration item being walked

const ANA_FRAMES = 8;   // AI frames averaged per point

function anaRedraw() { if (typeof renderRight === "function") { renderRight(); } }

function anaChannel(it) { return (it.port === "ain" ? "AI" : "AO") + it.channel; }
function anaIsCal(it) { return /\.cal\d$/.test(it.id); }
// On a configuration page the channel is on the configurations' shared range (decision 139).
function anaInfo(it) {
  if (typeof isCfgView === "function" && isCfgView(view) && stageCfgs && stageCfgs.channels) { return stageCfgs.channels[anaChannel(it)] || null; }
  return anaState && anaState.channels ? anaState.channels[anaChannel(it)] : null;
}

// Fetched once per connection and after every change; never per redraw.
function anaLoad(force) {
  const key = state.connected ? [state.port, state.firmware].join("|") : "";
  if (!key) { anaState = null; anaKey = ""; return; }
  if ((key === anaKey && !force) || anaLoading) { return; }
  anaKey = key; anaLoading = true;
  api("/api/analog").then(j => { anaState = j; anaLoading = false; anaRedraw(); },
                          () => { anaLoading = false; });
}


// The plan's per-point limit (the item's criteria step), as the card states it.
function anaLimit(it) {
  const cr = (it.criteria || [])[0] || {};
  return { step: cr.step, missing: !!cr.missing, check: (cr.checks || [])[0] };
}

// What the card states as its criterion: the plan's ±% FS through the board's
// formula, or no verdict for calibration (decision 99).
function anaCrit(it) {
  if (anaIsCal(it)) { return T("ana.crit_cal"); }
  const c = anaInfo(it), lim = anaLimit(it);
  if (!lim.check) { return T("shell.item.crit_missing", { step: lim.step || "-" }); }
  return T("ana.crit_read", { tol: lim.check.max, fs: c ? c.fs : "-", unit: c ? c.unit : "" });
}

function anaRangeName(r) { return T({ v: "ana.range.v", i: "ana.range.i", i20: "ana.range.i20", i420: "ana.range.i420" }[r] || "ana.range.v"); }
// DAC mV for an AO nominal, and the nominal of the frame's quantised DAC mV
// for the fit, both by the registered range's formula (rangeinfo.js, decision 136).
const anaAoMV = (v, c) => rngAODAC(v, rngSpec(c));
const anaAoWant = (f, n, c) => { const p = String(f["ch" + n] || "0/0/0").split("/"); return rngAONominal(Number(p[1]), rngSpec(c)); };
// AI nominal from the raw count on the registered range (calstore Range.AINominal).
const anaNominal = (raw, c) => rngAINominal(raw, rngSpec(c));
function anaFmt(v, unit) { return (typeof v === "number" && isFinite(v)) ? v.toFixed(unit === "mV" ? 1 : 3) : "-"; }

// Calibration defaults follow the range: AI five, AO three, low end to full
// scale (decisions 90, 94). Read / output defaults sit midway between them,
// topped up with 90 % of full scale to reach three (decision 107; C.3.2).
// The server uses the same rules (ptlib.FitPoints / DefaultPoints).
function anaFitDefaults(it) {
  const c = anaInfo(it);
  if (!c) { return []; }
  const n = it.port === "ain" ? 5 : 3;
  const lo = typeof c.min === "number" ? Math.max(c.lo, c.min) : c.lo;   // a 4-20 mA AO starts at what it can output (decision 136)
  const out = [];
  for (let k = 0; k < n; k++) { out.push(Math.round((lo + (c.fs - lo) * k / (n - 1)) * 100) / 100); }
  return out;
}
function anaDefaultPoints(it) {
  const fit = anaFitDefaults(it);
  if (anaIsCal(it)) { return fit.join(", "); }
  const out = [];
  for (let k = 1; k < fit.length; k++) { out.push(Math.round((fit[k - 1] + fit[k]) / 2 * 100) / 100); }
  if (fit.length && out.length < 3) { out.push(Math.round(anaInfo(it).fs * 0.9 * 100) / 100); }
  return out.join(", ");
}
function anaParse(text) {
  return String(text).split(/[,，\s]+/).filter(s => s !== "").map(Number).filter(v => isFinite(v));
}
function anaPointsText(it, view) {
  if (typeof isCfgView === "function" && isCfgView(view)) {
    const pts = cfgItem(view, it.id).points;
    return pts && pts.length ? pts.join(", ") : anaDefaultPoints(it);
  }
  return anaPtsText[it.id] !== undefined ? anaPtsText[it.id] : anaDefaultPoints(it);
}
function anaPoints(it, view) { return anaParse(anaPointsText(it, view)); }

// The fit points a test point is compared with: in R&D this channel's
// calibration card (walked, else as typed), elsewhere the defaults.
function anaFitPointsFor(it, view) {
  const cal = ITEM_BODIES[it.id.replace(/\.(read|out)(\d)$/, ".cal$2")] ? it.id.replace(/\.(read|out)(\d)$/, ".cal$2") : "";
  if (view === "rd" && cal) {
    const r = anaRes[cal];
    if (r && r.points && r.points.length) { return r.points.map(p => p.set); }
    if (anaPtsText[cal] !== undefined) { return anaParse(anaPtsText[cal]); }
  }
  return anaFitDefaults(it);
}

// A test point on a fit point only checks the residual: said, not refused
// (decision 107).
function anaOverlapNote(it, view, pts) {
  const fit = anaFitPointsFor(it, view);
  const same = pts.filter(v => fit.some(f => Math.abs(f - v) < 1e-9));
  const w = el("div", "ptwarn"); w.dataset.anaOverlap = it.id;
  w.hidden = !same.length;
  if (same.length) { w.textContent = "⚠️ " + T("ana.points_overlap", { pts: same.join(", "), unit: anaInfo(it).unit }); }
  return w;
}

// What R&D's Start sends for a read / output item: the typed points, or none
// for the server's defaults.
function anaPickPoints(it) { return anaPtsText[it.id] !== undefined ? anaParse(anaPtsText[it.id]) : undefined; }

// ---- params: range (editable in R&D only) and points ----------------------

// What the range block shows of the board's calibration for channel ch.
function anaKbOpts(ch) {
  const bc = (anaState && anaState.board) || {};
  const loc = ((anaState && anaState.local) || {})[ch];
  return { kb: (bc.ch || {})[ch] || null, calRange: loc ? loc.range : "",
           why: bc.why ? bc.why : T(ANA_BOARD[bc.state] || ANA_BOARD.unread) };
}

// The channel's range block (decision 136): in R&D with the range picker (it
// is the per-UID registration, decision 102), elsewhere a read-only summary
// that opens to the same block.
function anaRangeRow(it, view) {
  const c = anaInfo(it), ch = anaChannel(it);
  if (view !== "rd") { return [rngSummary(ch, c, anaKbOpts(ch))]; }
  return [rngBlock(ch, c, Object.assign({ select: anaRangeSelect(it) }, anaKbOpts(ch)))];
}

// Whether this board's ranges were registered or are the default.
function anaRangeHint() {
  return el("div", "hint", T(anaState.registered ? "ana.range_registered" : "ana.range_default", { uid: anaState.uid || "-" }));
}

// The range picker: changing it registers the range for this board's UID.
function anaRangeSelect(it) {
  const c = anaInfo(it), ch = anaChannel(it);
  const sel = el("select"); sel.dataset.range = ch;
  for (const s of c.specs || []) {
    const o = el("option", null, anaRangeName(s.id) + (s.default ? T("rng.default_mark") : ""));
    o.value = s.id; o.selected = s.id === c.range; sel.appendChild(o);
  }
  sel.onchange = async () => {
    const j = await api("/api/analog/ranges", { ranges: { [ch]: sel.value } });
    if (j.error) { showMessage(j.error, true); return; }
    anaState = j;
    // The unit and full scale changed: old points and results no longer apply.
    for (const id of Object.keys(ITEM_BODIES)) {
      if (id.startsWith(it.port + ".") && id.endsWith(String(it.channel))) { delete anaRes[id]; delete anaPtsText[id]; }
    }
    if (typeof anaCardForget === "function") { anaCardForget(it.port, it.channel); }
    // The card's session converts with the range it opened on: AO stops, a running AI read restarts (decision 137).
    if (typeof rdLive === "function" && rdLive(it.port)) {
      await rdEnd(it.port);
      if (it.port === "ain") { await rdRead("ain"); }
    }
    anaRedraw();
  };
  sel.disabled = !!(typeof anaCardBusy !== "undefined" && anaCardBusy)
    || (typeof libRunning === "function" && libRunning() && !(typeof rdOnlyCard === "function" && rdOnlyCard()));
  return sel;
}

function anaParams(it, view) {
  anaLoad(false);
  const box = el("div");
  const c = anaInfo(it);
  if (!c) { box.appendChild(el("div", "hint", T("ana.loading"))); return box; }
  for (const n of anaRangeRow(it, view)) { box.appendChild(n); }
  if (view === "rd") { box.appendChild(anaRangeHint()); }

  const pr = el("label", null, T(anaIsCal(it) ? "ana.cal_points" : "ana.test_points", { unit: c.unit }) + " ");
  const inp = el("input"); inp.dataset.anaPoints = it.id; inp.style.width = "200px";
  // Checked as typed; a bad list stays red and is neither kept nor sent (memo.js).
  const cfg = typeof isCfgView === "function" && isCfgView(view);
  pr.appendChild(memoCell(inp, "pts|" + it.id, () => anaPointsText(it, view), x => { if (!cfg) { anaPtsText[it.id] = x; } }));
  inp.onchange = () => {
    if (inp.classList.contains("badcell")) { return; }
    if (cfg) { cfgSetPoints(view, it, anaParse(inp.value)); return; }
    anaRedraw();
  };
  box.appendChild(pr);
  box.appendChild(el("div", "hint", T("ana.points_rule")));
  if (!anaIsCal(it)) { box.appendChild(anaOverlapNote(it, view, anaPoints(it, view))); }
  return box;
}

// ---- result ----------------------------------------------------------------

function anaKbLine(label, v, unit, none) {
  const d = el("div");
  d.appendChild(el("span", "sub", label + " "));
  d.appendChild(el("span", null, v ? T("ana.kb", { k: v.k.toFixed(5), b: v.b.toFixed(4), unit }) : none));
  return d;
}

const ANA_BOARD = { ok: "ana.board.ok", blank: "ana.board.blank", corrupt: "ana.board.corrupt",
  other: "ana.board.other", unread: "ana.board.unread" };

function anaCalLines(it) {
  const box = el("div");
  const c = anaInfo(it), ch = anaChannel(it);
  if (!c) { return box; }
  const local = (anaState.local || {})[ch];
  box.appendChild(anaKbLine(T("ana.local"), local, local ? local.unit : c.unit,
                            anaState.sim ? T("ana.local_sim") : T("ana.none")));
  const bc = anaState.board || {};
  const onBoard = (bc.ch || {})[ch];
  box.appendChild(anaKbLine(T("ana.onboard"), onBoard, c.unit,
                            bc.why ? bc.why : T(ANA_BOARD[bc.state] || ANA_BOARD.unread)));
  box.lastChild.dataset.anaBoard = bc.state || "unread";
  return box;
}

function anaResult(it, view) {
  anaLoad(false);
  const box = el("div");
  const c = anaInfo(it);
  if (anaIsCal(it)) {
    const r = anaRes[it.id];
    if (r && r.note) { box.appendChild(el("div", "msg" + (r.bad ? " bad" : ""), r.note)); }
    if (r && r.rows && r.rows.length) { box.appendChild(anaCalTable(r, c)); }
    if (r && r.fit) { box.appendChild(anaFitBox(r.fit)); }
    if (!r || (!r.rows && !r.note)) { box.appendChild(el("div", "hint", T("ana.cal_empty"))); }
    box.appendChild(anaCalLines(it));
    box.appendChild(anaButtons(it));
  } else {
    box.appendChild(anaReadBox(it));
    box.appendChild(anaCalLines(it));
  }
  return box;
}

function anaGrid(head, rows) {
  const grid = el("div", "rowgrid");
  const h = el("div", "gridrow hd2");
  for (const t of head) { h.appendChild(el("span", "c", t)); }
  grid.appendChild(h);
  for (const cells of rows) {
    const line = el("div", "gridrow");
    for (const [txt, cls] of cells) { line.appendChild(el("span", "c" + (cls ? " " + cls : ""), txt)); }
    grid.appendChild(line);
  }
  return grid;
}

function anaCalTable(r, c) {
  const u = c ? c.unit : "";
  return anaGrid([T("ana.col.point"), T("ana.col.set", { u }), T("ana.col.meter", { u })],
    r.rows.map((p, i) => [[String(i + 1)], [anaFmt(p.set, u)], [p.got === null ? T("ana.skipped") : anaFmt(p.got, u)]]));
}

// The regression and the final k, b; the residual is shown, never judged (decision 99).
function anaFitBox(f) {
  const box = el("div", "fitbox");
  if (!f.known) { box.appendChild(el("div", "msg bad", T("ana.fit_failed", { why: f.why || "" }))); return box; }
  const u = f.unit;
  box.appendChild(el("div", "calkb", T("ui.fitBox.final", { k: f.fit.gain.toFixed(5), b: f.fit.offset.toFixed(4), unit: u })));
  box.appendChild(calPlot(f.points, f.fit, u, T("ana.axis.nominal", { u }), T("ana.axis.measured", { u })));
  box.appendChild(el("div", "sub", T("ana.residual", {
    r: anaFmt(Math.abs(f.fit.max_residual), u), unit: u, pct: f.residual_pct_fs.toFixed(3) })));
  // A gain far from 1 is never stored (decision 106): most likely unsoldered jumpers.
  if (f.refuse) { const n = el("div", "msg bad", f.refuse); n.dataset.anaRefuse = "1"; box.appendChild(n); }
  return box;
}

// A read / output item's result as the server's item runner left it
// (GET /api/lib: states[id].analog, PORTTOOL-FLOW.md C.3.6).
function anaReadBox(it) {
  const box = el("div");
  const st = typeof libState === "function" ? libState(it.id) : null;
  if (!st) { box.appendChild(el("div", "hint", T("ana.read_empty"))); return box; }
  if (st.code === "not_calibrated") {
    const n = el("div", "msg bad", T("ana.not_calibrated_run", { why: st.detail || "" }));
    n.dataset.anaVerdict = "uncalibrated";
    box.appendChild(n);
    return box;
  }
  const a = st.analog;
  if (a && a.rows && a.rows.length) {
    const u = a.unit;
    // Raw, converted, reference and deviation per point (decision 108 「输出值显示」).
    // AO: converted is the current the board's k, b aim for, the reference is the meter;
    // AI: converted is the board's reading, the reference is the source.
    const ao = it.port === "aout", tol = (anaLimit(it).check || {}).max;
    const refHead = a.ref_ch ? "ana.col.ref_board" : "ana.col.ref";
    const raw = p => typeof p.raw === "number" ? p.raw.toFixed(0) : "-";
    const dev = p => [typeof p.dev === "number" ? p.dev.toFixed(2) : T("ana.skipped"),
                      p.pass === true ? "hit" : p.pass === false ? "wrong" : ""];
    const devHead = tol === undefined ? T("ana.col.dev_pct") : T("ana.col.dev_lim", { tol });
    box.appendChild(ao
      ? anaGrid([T("ana.col.raw_ao"), T("ana.col.cal_ao", { u }), T(refHead, { u }), devHead],
          a.rows.map(p => [[raw(p)], [anaFmt(p.set, u)], [p.got === null || p.got === undefined ? T("ana.skipped") : anaFmt(p.got, u)], dev(p)]))
      : anaGrid([T("ana.col.point_set", { u }), T("ana.col.raw_ai"), T("ana.col.conv_ai", { u }), T(refHead, { u }), devHead],
          a.rows.map(p => [[anaFmt(p.set, u)], [raw(p)], [anaFmt(p.shown, u)],
                           [p.got === null || p.got === undefined ? T("ana.skipped") : anaFmt(p.got, u)], dev(p)])));
  }
  if (st.running) { box.appendChild(el("div", "hint", T("lib.res.running"))); }
  if (a && (st.outcome === "PASS" || st.outcome === "FAIL")) {
    const pass = st.outcome === "PASS";
    const lim = anaLimit(it).check || {};
    const v = el("div", "msg" + (pass ? "" : " bad"),
                 T(pass ? "ana.pass" : "ana.fail", { tol: lim.max, fs: a.full_scale, unit: a.unit }));
    v.dataset.anaVerdict = pass ? "pass" : "fail";
    box.appendChild(v);
  }
  if (a) { box.appendChild(el("div", "sub", T("ana.used_kb", { r: anaRangeName(a.range), k: a.k.toFixed(5), b: a.b.toFixed(4) }))); }
  // The reference board gave the reference value with its own formula (STG-31).
  if (a && a.ref_ch) {
    box.appendChild(el("div", "sub", T("ana.used_kb_ref", { ch: a.ref_ch, r: anaRangeName(a.ref_range), k: a.ref_k.toFixed(5), b: (a.ref_b || 0).toFixed(4) })));
  }
  if (st.reason) { box.appendChild(el("div", "hint", st.reason)); }
  if (st.detail) { box.appendChild(el("div", "hint mono", T("lib.res.detail", { detail: st.detail }))); }
  return box;
}

// 存到本地 and 烧进板子: always shown, disabled with the reason (decision 97, C.3.2).
function anaButtons(it) {
  const ch = anaChannel(it);
  const r = anaRes[it.id];
  const fitted = r && r.fit && r.fit.known;
  const row = el("div", "row");

  const sv = el("button", null, T("ana.store")); sv.dataset.anaStore = it.id;
  const storeWhy = !fitted ? T("ana.why.no_fit") : r.fit.refuse || (anaState && anaState.store_why) || "";
  sv.disabled = !!storeWhy || !!(anaBtn.store && anaBtn.store.busy);
  sv.onclick = async () => {
    anaBtn.store = { busy: true, note: T("ana.storing") }; anaRedraw();
    const j = await api("/api/cal/store", { channel: ch, points: r.points });
    if (j.state) { anaState = j.state; }
    anaBtn.store = { busy: false, ok: !j.error,
                     note: j.error || T("ana.stored", { archive: j.stored.archive }) };
    anaRedraw();
  };
  row.appendChild(sv);

  const bn = el("button", null, T("ana.burn")); bn.dataset.anaBurn = it.id;
  const burnWhy = (anaState && anaState.burn_why) || "";
  bn.disabled = !!burnWhy || !!(anaBtn.burn && anaBtn.burn.busy);
  bn.onclick = async () => {
    anaBtn.burn = { busy: true, note: T("ui.cal.burning") }; anaRedraw();
    const j = await api("/api/cal/burn", {});
    anaBtn.burn = { busy: false, ok: !j.error, note: j.error || j.done };
    anaLoad(true);
    anaRedraw();
  };
  row.appendChild(bn);

  const box = el("div");
  box.appendChild(row);
  for (const [k, why, key] of [["store", storeWhy, "ana.why.store"], ["burn", burnWhy, "ana.why.burn"]]) {
    const b = anaBtn[k];
    const text = (b && b.note) || (why ? T(key, { why }) : "");
    if (!text) { continue; }
    const n = el("div", "tgtres" + (b && !b.busy && b.note ? (b.ok ? " ok" : " bad") : ""), text);
    n.dataset.anaWhy = k;
    box.appendChild(n);
  }
  return box;
}

// The calibration card's pill: a walk has no pass / fail, only fitted or not
// (decision 99).
function anaPill(it) {
  const r = anaRes[it.id];
  if (anaWalking === it.id) { return el("span", "pill run", T("lib.o.running")); }
  if (!r) { return el("span", "pill", T("shell.item.notrun")); }
  if (r.fit && r.fit.known) { return el("span", "pill rec", T("ana.pill.fitted")); }
  if (r.stopped) { return el("span", "pill skip", T("lib.o.stopped")); }
  return el("span", "pill fail", T("lib.o.error"));
}

// ---- calibration walk: a person at the meter or the source ----------------

async function anaWalkAI(it, pts, c) {
  const n = it.channel;
  await run("pt.stop all");
  await run("pt.start ain ch=" + n + " period=200");
  if (!await waitForNewFrames("ain", 1, 8000)) { await run("pt.stop all"); return { note: T("ana.no_frames"), bad: true }; }
  const rows = [];
  for (let i = 0; i < pts.length; i++) {
    const typed = await askMeter(T("ana.ask_ai", { ch: anaChannel(it), i: i + 1, n: pts.length, v: pts[i], unit: c.unit }),
                                 c.unit, T("ana.ask_ai_hint", { frames: ANA_FRAMES }));
    if (typed === null) { await run("pt.stop all"); return { rows, stopped: true }; }
    const raw = await aicalSampleRaw(n, ANA_FRAMES);
    const got = typed === "" ? null : Number(typed);
    rows.push({ set: pts[i], raw, got: (got === null || !isFinite(got)) ? null : got });
    anaRes[it.id] = { rows }; anaRedraw();
  }
  await run("pt.stop all");
  return { rows };
}

// Calibration drives the nominal itself: the board's k, b are what is being found.
async function anaWalkAO(it, pts, c) {
  const n = it.channel;
  await run("pt.stop all");
  await run("pt.start aout ch=" + n + " mv=" + n + ":0 period=500");
  if (!await waitForNewFrames("aout", 1, 8000)) { await run("pt.stop all"); return { note: T("ana.no_frames"), bad: true }; }
  const rows = [];
  for (let i = 0; i < pts.length; i++) {
    const mv = anaAoMV(pts[i], c);
    await run("pt.set aout mv=" + n + ":" + mv);
    await waitForNewFrames("aout", 2, 5000);
    const f = (reads.aout || {}).fields || {};
    const want = anaAoWant(f, n, c);
    const typed = await askMeter(T("ana.ask_ao", { ch: anaChannel(it), i: i + 1, n: pts.length, v: pts[i], unit: c.unit }),
                                 c.unit, T("ana.ask_ao_hint"));
    if (typed === null) { break; }
    const got = typed === "" ? null : Number(typed);
    rows.push({ set: pts[i], want, got: (got === null || !isFinite(got)) ? null : got });
    anaRes[it.id] = { rows }; anaRedraw();
  }
  await run("pt.set aout mv=" + n + ":0");
  await run("pt.stop all");
  return { rows, stopped: rows.length < pts.length };
}

// A calibration walk. Resolves true (fitted), false (could not walk or fit)
// or null (stopped by the person).
async function anaRun(it, view) {
  await new Promise(res => { anaLoad(false); const t = setInterval(() => { if (anaState || !state.connected) { clearInterval(t); res(); } }, 50); });
  const c = anaInfo(it);
  if (!c) { return false; }
  const pts = anaPoints(it, view);
  if (pts.length < 3 || pts.length > 6) {
    anaRes[it.id] = { note: T("ana.points_rule"), bad: true }; anaRedraw(); return false;
  }
  const ch = anaChannel(it);
  anaBtn = {};
  anaWalking = it.id;
  anaRes[it.id] = { rows: [] }; anaRedraw();
  try {
    const w = it.port === "ain" ? await anaWalkAI(it, pts, c) : await anaWalkAO(it, pts, c);
    if (w.note) { anaRes[it.id] = { note: w.note, bad: true }; return false; }
    if (w.stopped) { anaRes[it.id] = { rows: w.rows, note: T("ana.stopped"), stopped: true }; return null; }
    const points = w.rows.map(p => ({ set: p.set, raw: p.raw === undefined ? null : p.raw,
                                      want: p.want === undefined ? null : p.want, got: p.got }));
    const f = await api("/api/analog/fit", { channel: ch, points });
    anaRes[it.id] = { rows: w.rows, fit: f, points };
    return !!f.known;   // a fit always yields k and b (decision 99)
  } finally {
    anaWalking = "";
    anaRedraw();
  }
}

for (const id of ["ain.read1", "ain.read2", "aout.out1", "aout.out2"]) {
  ITEM_BODIES[id] = { params: anaParams, result: anaResult, crit: anaCrit };
}
for (const id of ["ain.cal1", "ain.cal2", "aout.cal1", "aout.cal2"]) {
  ITEM_BODIES[id] = { params: anaParams, result: anaResult, crit: anaCrit, run: anaRun, pill: anaPill };
}
