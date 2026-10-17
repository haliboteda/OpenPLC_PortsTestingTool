"use strict";

// The R&D AI / AO card: 「校准」 and 「实测」 blocks, each with its own start / stop,
// sessions run by rdvisit.js (decisions 117, 137, 138; PORTTOOL-FLOW.md C.3.6 「AI / AO 卡片」).
// Its members stay the catalogue's per-channel items, so configurations and calibration archives are unchanged.

const ANA_ROWS = 6;     // point-table rows; empty ones are not points (decision 117)

let anaActive = {};     // "cal" -> the AO point being output: "<item id>|<row>"
let anaCalRow = {};     // cal item id -> [{raw, want}] per table row
let anaTyped = {};      // "<item id>|<row>" -> a meter reading typed by the person
let anaMeas = {};       // AO output item id -> {actual}: the meter reading typed
let anaTarget = {};     // read / output item id -> the target typed
let anaCardBusy = "";   // what the card is doing on the board, for the buttons
let anaAiBlock = "";    // the AI block whose reading runs: "cal" | "meas"

// A range change invalidates what the card measured on that channel.
function anaCardForget(port, ch) {
  for (const id of Object.keys(anaCalRow)) { if (id.startsWith(port + ".cal" + ch)) { delete anaCalRow[id]; } }
  for (const id of Object.keys(anaMeas)) { if (id.endsWith(String(ch)) && id.startsWith(port + ".")) { delete anaMeas[id]; } }
}

const anaCalOf = (port, ch) => itemsOf("rd", port).find(it => it.id === port + ".cal" + ch);
const anaReadOf = (port, ch) => itemsOf("rd", port).find(it => it.id === (port === "ain" ? "ain.read" : "aout.out") + ch);

function anaPortCard(port) {
  anaLoad(false);
  const p = port.port;
  const card = el("div", "item anacard"); card.dataset.anacard = p;
  const hd = el("div", "ih");
  hd.appendChild(el("b", null, port.label));
  card.appendChild(hd);
  if (!anaState) { card.appendChild(el("div", "hint", T("ana.loading"))); return card; }

  const chans = [1, 2].filter(ch => anaCalOf(p, ch) || anaReadOf(p, ch));
  card.appendChild(anaRangesBlock(p, chans));
  card.appendChild(anaCalBlock(p, chans));
  card.appendChild(anaMeasBlock(p, chans));
  return card;
}

/* ---------------- 档位与换算 ---------------- */

// On top of the card, each channel's range block with its picker (decision 136).
function anaRangesBlock(p, chans) {
  const sec = el("div", "anablk"); sec.dataset.anablk = "range";
  const sh = el("div", "khead");
  sh.appendChild(el("b", null, T("rng.title")));
  sec.appendChild(sh);
  sec.appendChild(anaRangeHint());
  const grid = el("div", "anachans");
  for (const ch of chans) {
    const it = anaCalOf(p, ch) || anaReadOf(p, ch);
    for (const n of anaRangeRow(it, "rd")) { grid.appendChild(n); }
  }
  sec.appendChild(grid);
  return sec;
}

/* ---------------- 1 校准 ---------------- */

function anaPtsOf(it) {
  const pts = anaParse(anaPtsText[it.id] !== undefined ? anaPtsText[it.id] : anaDefaultPoints(it));
  const out = pts.slice(0, ANA_ROWS);
  while (out.length < ANA_ROWS) { out.push(null); }
  return out;
}

function anaCalBlock(p, chans) {
  const sec = el("div", "anablk"); sec.dataset.anablk = "cal";
  const sh = el("div", "khead");
  sh.appendChild(el("b", null, "1  " + T("ana.blk.cal")));
  if (p === "ain") { sh.appendChild(anaReadBtn("cal")); }
  sh.appendChild(el("span", "sp"));
  sec.appendChild(sh);
  sec.appendChild(el("div", "hint", T(p === "ain" ? "ana.cal_hint_ai" : "ana.cal_hint_ao")));
  // 记录该点 averages the running reading: none runs until this block's 开始.
  if (p === "ain") { sec.appendChild(anaReadNote("cal")); }
  const grid = el("div", "anachans");
  for (const ch of chans) {
    const it = anaCalOf(p, ch);
    if (it) { grid.appendChild(anaCalChan(it)); }
  }
  sec.appendChild(grid);
  return sec;
}

function anaCalChan(it) {
  const c = anaInfo(it), u = c ? c.unit : "";
  const box = el("div", "anachan"); box.dataset.anachan = it.id;
  const nm = el("div", "row"); nm.appendChild(el("b", null, anaChannel(it)));
  box.appendChild(nm);
  if (!c) { return box; }
  box.appendChild(el("div", "hint", anaRangeName(c.range) + " (" + c.unit + ")"));

  const pts = anaPtsOf(it);
  const rows = anaCalRow[it.id] || [];
  const ao = it.port === "aout";
  const act = T(ao ? "ana.cal.out" : "ana.cal.take");
  const tbl = el("table", "gtab anapts");
  const hr = el("tr");
  for (const h of [T("ana.col.pt", { u }), T("ana.col.measured"), T("ana.col.err_ref", { tol: CAL_REF_TOL_PCT }), act]) { hr.appendChild(el("th", null, h)); }
  tbl.appendChild(hr);
  pts.forEach((v, i) => {
    const key = it.id + "|" + i;
    const tr = el("tr"); tr.dataset.row = i;
    const r = rows[i] || {};
    const pin = el("input"); pin.className = "pt"; pin.dataset.anapt = key;
    pin.disabled = !!anaCardBusy;
    // Only good points make up the list; a red cell is left out (C.3.6 「填数超量程」).
    const pcell = memoCell(pin, "pt|" + key, () => (v === null ? "" : String(v)), () => {
      const all = [...document.querySelectorAll('[data-anapt^="' + it.id + '|"]')].filter(x => !x.classList.contains("badcell"))
        .map(x => x.value.trim()).filter(x => x !== "");
      anaPtsText[it.id] = all.join(", ");
      if (anaCalRow[it.id]) { anaCalRow[it.id][i] = undefined; }
      delete anaRes[it.id];
    });
    pin.onchange = () => { if (!pin.classList.contains("badcell")) { renderRight(); } };
    tr.appendChild(el("td")).appendChild(pcell);

    const got = el("td");
    let meas = null;
    if (!ao && typeof r.raw === "number") { meas = anaNominal(r.raw, c); got.appendChild(el("b", "gotval", anaFmt(meas, u))); }
    if (ao) {
      const typed = el("input"); typed.className = "pt"; typed.dataset.anagot = key; typed.type = "number"; typed.step = "any";
      typed.disabled = !!anaCardBusy;
      got.appendChild(memoCell(typed, "met|" + key, () => anaTyped[key], x => { anaTyped[key] = x; }));
      typed.onchange = () => { if (!typed.classList.contains("badcell")) { renderRight(); } };
      const t = Number(anaTyped[key]);
      if (anaTyped[key] !== undefined && anaTyped[key] !== "" && isFinite(t)) { meas = t; }
    }
    tr.appendChild(got);
    // A row with a measured value is a recorded point (decision 151).
    if (meas !== null) { tr.classList.add("done"); tr.dataset.done = "1"; }
    // Reference only: marked against ±1 % FS, never blocks 存到本地 (STG-12).
    const errOk = v !== null && meas !== null ? Math.abs((meas - v) / c.fs * 100) <= CAL_REF_TOL_PCT : null;
    const err = el("td", errOk === null ? null : errOk ? "ok" : "bad",
      errOk === null ? "" : (errOk ? "✅ " : "❌ ") + anaErrText(meas, v, c.fs));
    err.dataset.anaerr = key;
    tr.appendChild(err);

    // AI: takes the calibration block's reading; AO: outputs this point, one at
    // a time, and pressing the point in effect again cancels it (decision 151).
    const active = ao && anaActive.cal === key;
    const b = el("button", active ? "act on" : "act", active ? T("ana.cal.out_on") : act); b.dataset.anaset = key;
    b.disabled = v === null || !!anaCardBusy || (ao ? libRunning() && !rdLive("aout") : !anaAiOn("cal"));
    if (!ao && !anaAiOn("cal")) { b.title = T("ana.cal_need_read"); }
    if (active) { b.dataset.active = "1"; }
    b.onclick = () => (active ? anaCalCancel(it) : anaCalSet(it, i, v));
    tr.appendChild(el("td")).appendChild(b);
    tbl.appendChild(tr);
  });
  box.appendChild(tbl);

  const fit = el("div", "anafit");
  const fb = el("button", null, T("ana.fit")); fb.dataset.anafit = it.id;
  fb.disabled = !!anaCardBusy;
  fb.onclick = () => anaFit(it);
  fit.appendChild(fb);
  fit.appendChild(el("span", "hint", T("ana.fit_method")));
  const res = anaRes[it.id];
  if (res && res.fit && res.fit.known) {
    fit.appendChild(el("b", "calkb", T("ui.fitBox.final", { k: res.fit.fit.gain.toFixed(5), b: res.fit.fit.offset.toFixed(4), unit: res.fit.unit })));
  }
  box.appendChild(fit);
  if (res && res.note) { box.appendChild(el("div", "msg" + (res.bad ? " bad" : ""), res.note)); }
  if (res && res.fit && !res.fit.known) { box.appendChild(el("div", "msg bad", T("ana.fit_failed", { why: res.fit.why || "" }))); }
  if (res && res.fit && res.fit.refuse) { const n = el("div", "msg bad", res.fit.refuse); n.dataset.anaRefuse = "1"; box.appendChild(n); }
  box.appendChild(anaButtons(it));
  box.appendChild(anaCalLines(it));
  return box;
}

// The calibration table's per-point reference, AI and AO alike (STG-12).
const CAL_REF_TOL_PCT = 1;

function anaErrText(got, want, fs) {
  const pct = (got - want) / fs * 100;
  return (pct >= 0 ? "+" : "") + pct.toFixed(2) + " %";
}

// AI 记录该点: the person has set the source to this point; the running
// reading is averaged. AO 输出该点: the board outputs the point's nominal,
// the person reads the meter; pressing another releases it.
async function anaCalSet(it, i, v) {
  if (memoBlocked()) { return; }
  const n = it.channel, key = it.id + "|" + i;
  if (it.port === "aout") { anaActive.cal = key; }
  anaCardBusy = key; renderRight();
  try {
    const rows = anaCalRow[it.id] = anaCalRow[it.id] || [];
    if (it.port === "ain") {
      const raw = await aicalSampleRaw(n, ANA_FRAMES);
      if (raw === null) { anaRes[it.id] = { note: T("ana.no_frames"), bad: true }; return; }
      rows[i] = { raw };
    } else {
      if (rdLive("aout")) { await rdEnd("aout"); }
      const c = anaInfo(it), mv = anaAoMV(v, c);
      if (!(reads.aout && anaAoOn[n])) { await run("pt.start aout ch=" + n + " mv=" + n + ":" + mv + " period=500"); anaAoOn[n] = true; }
      await run("pt.set aout mv=" + n + ":" + mv);
      await waitForNewFrames("aout", 2, 5000);
      const f = (reads.aout || {}).fields || {};
      rows[i] = { want: anaAoWant(f, n, c) };
    }
    rdTouch();
    delete anaRes[it.id];
  } finally {
    anaCardBusy = "";
    renderRight();
  }
}
let anaAoOn = {};   // AO channel -> its session was started by the card

// AO: the point in effect pressed again goes back to 0 (decision 151).
async function anaCalCancel(it) {
  anaCardBusy = it.id; renderRight();
  try { await anaAoOff(it.channel); } finally { anaCardBusy = ""; renderRight(); }
}

async function anaAoOff(n) {
  if (!anaAoOn[n]) { return; }
  await run("pt.set aout mv=" + n + ":0");
  await run("pt.stop aout");
  anaAoOn[n] = false;
  if (anaActive.cal && anaActive.cal.startsWith("aout.cal" + n + "|")) { anaActive.cal = ""; }
}

// The points the fit gets: AI the reading as nominal and the point as the
// instrument; AO the frame's nominal and the meter (PORTTOOL-FLOW.md C.3.6).
function anaFitPointsOf(it) {
  const pts = anaPtsOf(it), rows = anaCalRow[it.id] || [], out = [];
  pts.forEach((v, i) => {
    const r = rows[i];
    if (v === null || !r) { return; }
    if (it.port === "ain" && typeof r.raw === "number") { out.push({ set: v, raw: r.raw, want: null, got: v }); }
    if (it.port === "aout") {
      const t = Number(r.got !== undefined ? r.got : anaTyped[it.id + "|" + i]);
      if (typeof r.want === "number" && isFinite(t) && String(r.got ?? anaTyped[it.id + "|" + i] ?? "") !== "") {
        out.push({ set: v, raw: null, want: r.want, got: t });
      }
    }
  });
  return out;
}

async function anaFit(it) {
  const points = anaFitPointsOf(it);
  if (it.port === "aout") { await anaAoOff(it.channel); }
  if (points.length < 3) { anaRes[it.id] = { note: T("ana.fit_need"), bad: true }; renderRight(); return; }
  const f = await api("/api/analog/fit", { channel: anaChannel(it), points });
  anaRes[it.id] = { rows: points, fit: f, points };
  anaBtn = {};
  renderRight();
}

/* ---------------- 2 实测 ---------------- */

// Both channels are judged, no ticks (decision 151); AI's only after its 实测
// block read, the calibration block's reading records nothing.
function anaTickedOf(port) {
  const all = [1, 2].filter(ch => anaReadOf(port, ch));
  return { "": port === "aout" || anaAiBlock === "meas" ? all : [] };
}

const anaAiOn = block => !!rdLive("ain") && anaAiBlock === block;

// 开始 / 停止 of one AI block; starting one ends the other first.
function anaReadBtn(block) {
  const on = anaAiOn(block);
  const b = el("button", on ? "stop" : "primary", T(on ? "ana.blk_stop" : "ana.blk_start"));
  b.dataset.anaread = block;
  b.disabled = rdBusy || !!rdStarting || libPageBusy || !!anaCardBusy || !state.connected || (libRunning() && !rdLive("ain"));
  b.onclick = () => anaRead(block);
  return b;
}

async function anaRead(block) {
  if (rdLive("ain")) {
    const mine = anaAiBlock === block;
    rdBusy = true; renderRight();
    try { await rdEnd("ain"); } finally { rdBusy = false; renderRight(); }
    if (mine) { return; }
  }
  if (memoBlocked()) { return; }
  anaAiBlock = block;
  rdRead("ain");
}

// Said in an AI block while its reading is not running.
function anaReadNote(block) {
  if (anaAiOn(block) || (rdStarting && anaAiBlock === block)) { return el("span"); }
  const why = anaAiBlock === block ? rdReadWhy : "";
  const n = el("div", "hint", why ? T("rd.read_failed", { why }) : T(block === "cal" ? "ana.cal_need_read" : "ana.meas_idle"));
  n.dataset.rdnote = "ain|" + block;
  if (block === "cal") { n.dataset.calneedread = "1"; }
  return n;
}

// The board's k, b the session read at its start (decision 151).
function anaKbText(a, lv) {
  if (!lv || !a || a.ch === undefined) { return "-"; }
  if (typeof a.k === "number" && typeof a.b === "number") { return "k=" + a.k.toFixed(5) + "  b=" + a.b.toFixed(4); }
  return T("ana.kb_none");
}

// What the person typed, sent to a session as it opens (rdvisit.js): AI the
// targets, AO the meter readings.
function anaRefs(port) {
  const out = [];
  for (const ch of [1, 2]) {
    const it = anaReadOf(port, ch);
    if (!it) { continue; }
    const v = port === "ain" ? anaTarget[it.id] : (anaMeas[it.id] || {}).actual;
    if (v !== undefined && v !== "") { out.push({ block: "", ch, value: v }); }
  }
  return out;
}

function anaMeasBlock(p, chans) {
  const sec = el("div", "anablk"); sec.dataset.anablk = "meas";
  const sh = el("div", "khead");
  sh.appendChild(el("b", null, "2  " + T("ana.blk.meas")));
  sh.appendChild(p === "aout" ? anaOutBtn() : anaReadBtn("meas"));
  sh.appendChild(el("span", "sp"));
  sec.appendChild(sh);
  sec.appendChild(el("div", "hint", T(p === "ain" ? "ana.meas_hint_ai" : "ana.meas_hint_ao")));
  if (p === "ain") { sec.appendChild(anaReadNote("meas")); }
  // The AI session belongs to this block only while it reads for it.
  const lv = p === "ain" && !anaAiOn("meas") ? null : rdLive(p);
  if (lv) { sec.appendChild(el("div", "hint", T("lib.live.frames", { n: lv.frames }))).dataset.frames = String(lv.frames); }

  const tol = liveTol(p, 1);
  const devHead = tol === undefined ? T("ana.col.dev_pct") : T("ana.col.dev_lim", { tol });
  const heads = p === "ain"
    ? [T("ana.col.ch"), T("ana.col.kb"), T("lib.live.col.raw_ai"), T("ana.col.conv"), T("ana.col.target"), devHead, T("lib.live.col.match")]
    : [T("ana.col.ch"), T("ana.col.kb"), T("ana.col.target"), T("ana.col.board_out"), T("ana.col.actual"), devHead, T("lib.live.col.match")];
  const tbl = el("table", "gtab anameas");
  const hr = el("tr");
  for (const h of heads) { hr.appendChild(el("th", null, h)); }
  tbl.appendChild(hr);
  for (const ch of chans) {
    const it = anaReadOf(p, ch);
    if (it) { tbl.appendChild(anaMeasRow(it, lv)); }
  }
  sec.appendChild(tbl);
  for (const a of (lv && lv.analog) || []) {
    if (!a.unit) { sec.appendChild(el("div", "hint", T("lib.live.no_range"))); break; }
    if (!a.calibrated) { sec.appendChild(el("div", "hint", T("lib.live.not_cal", { ch: (p === "ain" ? "AI" : "AO") + a.ch }))); }
  }
  // The last round's per-channel results, once the session has ended.
  const st = rdState(p), sit = (catalog.items || []).find(x => x.id === RD_SESSION[p].id);
  if (st && !st.running && sit && (st.chans || []).length) {
    const d = el("div", "anares"); d.dataset.item = sit.id; d.appendChild(libPill(sit)); sec.appendChild(d);
  }
  return sec;
}

function anaMeasRow(it, lv) {
  const c = anaInfo(it), u = c ? c.unit : "";
  const ch = it.channel, ao = it.port === "aout";
  const a = ((lv && lv.analog) || []).find(x => x.ch === ch) || {};
  const r = ((lv && lv.ref) || []).find(x => x.ch === ch);
  const tr = el("tr"); tr.dataset.item = it.id;
  tr.appendChild(el("td", "mono", anaChannel(it)));
  tr.appendChild(el("td", "mono", anaKbText(a, lv))).dataset.anakb = String(ch);

  const tgt = el("input"); tgt.className = "pt"; tgt.type = "number"; tgt.step = "any"; tgt.dataset.anatarget = it.id;
  const def = anaParse(anaDefaultPoints(it))[0];
  if (def !== undefined) { tgt.placeholder = String(def); }
  const ttd = el("td");
  ttd.appendChild(memoCell(tgt, "tgt|" + it.id, () => anaTarget[it.id], x => { anaTarget[it.id] = x; }));
  ttd.appendChild(el("span", "hint", " " + u));
  // Typed any time; a good value goes out at once (decision 151): AI's is the
  // reference the reading is judged against, AO's is output again.
  tgt.addEventListener("input", () => {
    if (tgt.classList.contains("badcell")) { return; }
    if (!ao) { rdRef("ain", "", ch, anaTarget[it.id] ?? ""); return; }
    if (rdLive("aout") && anaTarget[it.id] !== undefined && anaTarget[it.id] !== "") { anaOutOne(ch); }
  });

  const num = (x, d) => (typeof x === "number" ? x.toFixed(d) : null);
  if (!ao) {
    const raw = (num(a.raw, 0) ?? "-") + " / " + (a.pin_mv !== undefined && a.pin_mv !== null ? a.pin_mv + " mV" : "-");
    tr.appendChild(el("td", "mono", lv ? raw : "-"));
    tr.appendChild(el("td", "mono", lv ? (num(a.value, a.unit === "mV" ? 0 : 3) !== null ? num(a.value, a.unit === "mV" ? 0 : 3) + " " + a.unit : T("lib.live.no_conv")) : "-"));
    tr.appendChild(ttd);
  } else {
    tr.appendChild(ttd);
    const out = typeof a.set === "number" && typeof a.raw === "number" && typeof a.value === "number"
      ? a.raw + " mV → " + a.value.toFixed(a.unit === "mV" ? 0 : 3) + " " + a.unit : "-";
    tr.appendChild(el("td", "mono", out)).dataset.aoval = String(ch);
    const typed = el("input"); typed.className = "pt"; typed.type = "number"; typed.step = "any"; typed.dataset.anaactual = it.id;
    const atd = el("td");
    atd.appendChild(memoCell(typed, "act|" + it.id, () => (anaMeas[it.id] || {}).actual, x => { (anaMeas[it.id] = anaMeas[it.id] || {}).actual = x; }));
    typed.addEventListener("input", () => { if (!typed.classList.contains("badcell")) { rdRef("aout", "", ch, (anaMeas[it.id] || {}).actual ?? ""); } });
    tr.appendChild(atd);
  }
  const dev = el("td", "mono", r && typeof r.dev === "number" ? (r.dev >= 0 ? "+" : "") + r.dev.toFixed(2) + " % FS" : "-");
  tr.appendChild(dev);
  const ok = r && (r.pass === true || r.pass === false) ? r.pass : null;
  const m = el("td", ok === null ? "mono" : ok ? "mono ok" : "mono bad",
    ok === null ? "-" : (ok ? "✅ " : "❌ ") + T(ok ? "lib.live.pass" : "lib.live.fail"));
  m.dataset.cmp = String(ch); m.dataset.pass = ok === null ? "" : ok ? "1" : "0";
  tr.appendChild(m);
  return tr;
}

// AO 实测 开始 / 停止: both channels at their own targets (decisions 137, 138).
function anaOutBtn() {
  const on = !!rdLive("aout");
  const b = el("button", on ? "stop" : "primary", T(on ? "ana.blk_stop" : "ana.blk_start"));
  b.dataset.rdout = "aout";
  b.disabled = rdBusy || libPageBusy || (!!anaCardBusy && anaCardBusy !== "out") || (libRunning() && !on);
  b.onclick = anaOut;
  return b;
}

async function anaOut() {
  if (rdLive("aout")) { await rdEnd("aout"); renderRight(); return; }
  if (memoBlocked()) { return; }
  const chs = [1, 2].filter(ch => anaReadOf("aout", ch));
  for (const ch of chs) {
    const t = anaTarget[(anaReadOf("aout", ch) || {}).id];
    if (t === undefined || t === "" || !isFinite(Number(t))) { showMessage(T("ana.need_target", { ch: "AO" + ch }), true); return; }
  }
  anaCardBusy = "out"; renderRight();
  try {
    await anaAoOff(1); await anaAoOff(2);
    const why = await rdStart("aout");
    if (why) { showMessage(why, true); return; }
    for (const ch of chs) { await anaOutOne(ch, true); }
  } finally {
    anaCardBusy = "";
    libRefresh();
  }
}

// One AO channel to its typed target on the running session.
async function anaOutOne(ch, quiet) {
  const j = await api("/api/lib/live", { id: "aout.manual", op: "ao", ch, ma: Number(anaTarget[anaReadOf("aout", ch).id]) });
  if (j.error) { showMessage(j.error, true); }
  if (!quiet) { libRefresh(); }
}
