"use strict";

// The R&D DI / DO card: DI reads in one din.quad session, DO outputs one table at a
// time via dout.manual (decisions 119, 133, 137, 138; PORTTOOL-FLOW.md C.3.6 「DI / DO 卡片」).
// Sessions are rdvisit.js's; the server judges (live.go).

let dioStd = {};      // "<table>|<ch>" -> standard value chosen / typed (level, load, blink, pwmok)
let dioPwm = { res: "8", rows: {} };   // ch -> {on, freq, duty}

const dioItem = id => itemsOf("rd", id.split(".")[0]).find(it => it.id === id);

function dioPortCard(port) {
  const p = port.port;
  const card = el("div", "item anacard diocard"); card.dataset.diocard = p;
  const hd = el("div", "ih");
  hd.appendChild(el("b", null, port.label));
  if (p === "din") { hd.appendChild(el("span", "sp")); hd.appendChild(rdReadBtn("din")); }
  card.appendChild(hd);
  if (p === "din") {
    card.appendChild(rdReadNote("din"));
    const lv = rdLive("din");
    if (lv) { card.appendChild(el("div", "hint", T("lib.live.frames", { n: lv.frames }))).dataset.frames = String(lv.frames); }
    if (dioItem("din.level")) { card.appendChild(dioDiLevel(lv)); }
    if (dioItem("din.quad")) { card.appendChild(dioDiQuad(lv)); }
  } else {
    const lv = rdLive("dout");
    if (dioItem("dout.load")) { card.appendChild(dioDoLoad(lv)); }
    if (dioItem("dout.blink")) { card.appendChild(dioDoBlink(lv)); }
    if (dioItem("dout.square")) { card.appendChild(dioDoPwm(lv)); }
  }
  return card;
}

/* ---------------- shared pieces ---------------- */

// A table's head: number and name, and on DO its 输出 / 停止输出.
function dioHead(n, name, outBlock) {
  const sec = el("div", "anablk");
  const sh = el("div", "khead");
  sh.appendChild(el("b", null, n + "  " + name));
  if (outBlock) { sh.appendChild(dioOutBtn(outBlock)); }
  sh.appendChild(el("span", "sp"));
  sec.appendChild(sh);
  return sec;
}

function dioTable(heads) {
  const tbl = el("table", "gtab diotab"), tr = el("tr");
  for (const h of heads) { tr.appendChild(el("th", null, h)); }
  tbl.appendChild(tr);
  return tbl;
}

// A table's typed row as the server judged it: matched or not.
function dioCmp(rows, ch, words) {
  const r = (rows || []).find(x => x.ch === ch);
  const ok = r && (r.pass === true || r.pass === false) ? r.pass : null;
  const td = el("td", ok === null ? "" : ok ? "ok" : "bad",
    ok === null ? "-" : (ok ? "✅ " : "❌ ") + T(ok ? words[0] : words[1]));
  td.dataset.cmp = String(ch); td.dataset.pass = ok === null ? "" : ok ? "1" : "0";
  return td;
}
const DIO_MATCH = ["lib.live.match", "lib.live.mismatch"];
const DIO_JUDGE = ["lib.live.pass", "lib.live.fail"];

// One editable parameter of an item, as on a configuration; a change reaches
// a running session at once when onSet is given.
function dioParam(id, k, onSet) {
  const it = dioItem(id);
  if (!it) { return el("span"); }
  const box = libParamEditor(Object.assign({}, it, { params: [k] }));
  if (onSet) { box.addEventListener("change", () => { setTimeout(onSet, 0); }); }
  return box;
}
const dioParamOf = (id, k) => (libEdits[id] || {})[k] ?? ((libData.defaults || {})[id] || {})[k];

// The table's results once its session ended (live.go liveTicked).
function dioResult(sec, id) {
  const it = dioItem(id), st = libState(id);
  if (!it || !st || st.running || !(st.chans || []).length) { return; }
  const d = el("div", "anares"); d.dataset.item = id;
  d.appendChild(libPill(it));
  sec.appendChild(d);
}

// What the person typed, sent to a session as it opens (rdvisit.js).
function dioRefs(port) {
  const out = [];
  const put = (block, ch, v) => { if (v !== undefined && v !== "") { out.push({ block, ch, value: v }); } };
  if (port === "din") {
    for (let ch = 1; ch <= 8; ch++) { put("level", ch, dioStd["level|" + ch]); }
    for (let g = 1; g <= 4; g++) { put("", g, liveRefTyped["din.quad|" + g]); }
    return out;
  }
  const typed = { drop: "load", blink: "blink", pwm: "pwmok" }[rdDoBlock];
  for (let ch = 1; typed && ch <= 8; ch++) { put(rdDoBlock, ch, dioStd[typed + "|" + ch]); }
  return out;
}

/* ---------------- DI 逐路电平 ---------------- */

// All eight are always judged; a row with no standard level is stopped, so
// none chosen records eight stopped (decision 151 item 4, STG-40 row 1).
function dioLevelJudged() { return [1, 2, 3, 4, 5, 6, 7, 8]; }

// The sample period is the session's, shared by all eight in the firmware.
function dioPeriodSet() {
  const v = dioParamOf("din.level", "period");
  if (v !== undefined && v !== "") { rdSet("din", { period: String(v) }); }
}

function dioDiLevel(lv) {
  const sec = dioHead("1", T("dio.blk.level"));
  sec.dataset.dioblk = "level";
  sec.appendChild(el("div", "hint", T("dio.level_hint_live")));
  sec.appendChild(dioParam("din.level", "period", dioPeriodSet));
  const rows = (lv && lv.blocks || {}).level;
  // No ticks: all eight are read and judged (decision 151 item 4).
  const tbl = dioTable([T("lib.live.col.ch"), T("dio.col.board_level"), T("dio.col.std_level"), T("lib.live.col.match")]);
  for (let ch = 1; ch <= 8; ch++) {
    const key = "level|" + ch, tr = el("tr"); tr.dataset.ch = String(ch);
    tr.appendChild(el("td", "mono", "DI" + ch));
    const got = el("td");
    if (lv && lv.bits) {
      const on = lv.bits[ch - 1];
      const lamp = el("span", on ? "lamp on" : "lamp", T(on ? "lib.live.level.hi" : "lib.live.level.lo"));
      lamp.dataset.di = String(ch); lamp.dataset.on = on ? "1" : "0";
      got.appendChild(lamp);
    } else { got.textContent = "-"; }
    tr.appendChild(got);
    const std = el("select"); std.dataset.diostd = key; std.dataset.liveref = "level|" + ch;
    std.appendChild(new Option(T("lib.live.ref_unset"), ""));
    std.appendChild(new Option(T("lib.live.level.hi"), "1"));
    std.appendChild(new Option(T("lib.live.level.lo"), "0"));
    std.value = dioStd[key] ?? "";
    std.onchange = () => { dioStd[key] = std.value; std.blur(); rdRef("din", "level", ch, std.value); };
    tr.appendChild(el("td")).appendChild(std);
    tr.appendChild(dioCmp(rows, ch, DIO_MATCH));
    tbl.appendChild(tr);
  }
  sec.appendChild(tbl);
  dioResult(sec, "din.level");
  return sec;
}

/* ---------------- DI 编码器计数 ---------------- */

function dioDiQuad(lv) {
  const it = dioItem("din.quad");
  const sec = dioHead("2", T("dio.blk.quad"));
  sec.dataset.dioblk = "quad";
  const what = el("div", "hint", T("dio.quad_what")); what.dataset.quadwhat = "1";
  sec.appendChild(what);
  sec.appendChild(el("div", "hint", T("dio.quad_hint_live")));
  const tbl = dioTable(["", T("lib.quad.col.group"), T("lib.quad.col.pins"), T("dio.col.counted"), T("lib.live.col.dir"), T("dio.col.std_steps"), T("lib.live.col.match")]);
  for (let g = 1; g <= 4; g++) {
    const tr = el("tr"); tr.dataset.group = String(g);
    tr.appendChild(el("td")).appendChild(rdTickBox("quad", g));
    tr.appendChild(el("td", "mono", String(g)));
    tr.appendChild(el("td", "mono", "DI" + (2 * g - 1) + " / DI" + 2 * g));
    const q = lv && (lv.groups || [])[g - 1];
    tr.appendChild(el("td", "mono", q ? String(q.count) : "-"));
    tr.appendChild(el("td", null, !q ? "-" : T(q.dir > 0 ? "lib.live.dir.up" : q.dir < 0 ? "lib.live.dir.down" : "lib.live.dir.still")));
    // A typed cell: checked, kept only when good (memo.js); sent when it changes.
    const f = el("input"); f.type = "number"; f.step = "1"; f.className = "pt"; f.dataset.liveref = "din.quad|" + g;
    const key = "din.quad|" + g;
    const w = memoCell(f, "quad|" + g, () => liveRefTyped[key], x => { liveRefTyped[key] = x; });
    f.onchange = () => { if (!f.classList.contains("badcell")) { rdRef("din", "", g, f.value); } };
    tr.appendChild(el("td")).appendChild(w);
    tr.appendChild(dioCmp(lv && lv.ref, g, DIO_MATCH));
    tbl.appendChild(tr);
  }
  sec.appendChild(tbl);
  if (lv) {
    sec.appendChild(el("div", "chk " + (lv.err ? "bad" : "ok"), T("lib.quad.err", { n: lv.err })));
    const r = el("button", null, T("lib.live.reset")); r.dataset.livereset = "din.quad";
    r.onclick = async () => { await liveAct("din.quad", { op: "reset" }); dioPeriodSet(); };
    sec.appendChild(r);
  }
  if (it) { dioResult(sec, "din.quad"); }
  return sec;
}

/* ---------------- DO: one 输出 at a time ---------------- */

function dioOutBtn(block) {
  const on = !!rdLive("dout") && rdDoBlock === block;
  const b = el("button", on ? "stop" : "primary", T(on ? "rd.out_stop" : "rd.out"));
  b.dataset.rdout = "dout:" + block;
  b.disabled = rdBusy || libPageBusy || (libRunning() && !on);
  b.onclick = () => dioOut(block);
  return b;
}

// The duty list for all eight: the ticked at their own, the rest off.
function dioDuties(chs, duty) {
  const out = [];
  for (let ch = 1; ch <= 8; ch++) { out.push(ch + ":" + (chs.includes(ch) ? duty(ch) : 0)); }
  return out.join(",");
}

// The pt.set arguments of a table's 输出: only the ticked channels output
// (decision 151 item 9). Returns {args} or {why}.
function dioOutArgs(block, chs) {
  if (block === "drop" || block === "blink") {
    const args = { mode: block === "drop" ? "hold" : "blink", res: "8", duty: dioDuties(chs, () => 255) };
    const per = dioParamOf(block === "drop" ? "dout.load" : "dout.blink", "period");
    if (per) { args.period = String(per); }
    return { args };
  }
  if (!chs.length) { return { args: { mode: "hold", res: dioPwm.res, duty: dioDuties(chs, () => 0) } }; }
  const why = dioPwmProblem();
  if (why) { return { why }; }
  return { args: { mode: "hold", res: dioPwm.res, duty: dioDuties(chs, ch => dioPwm.rows[ch].duty),
    freq: chs.map(ch => ch + ":" + dioPwm.rows[ch].freq).join(",") } };
}

// A tick changed while its table outputs: the board follows the ticks at once.
async function dioRetick(table) {
  if (!rdLive("dout") || rdDoBlock !== table) { return; }
  const o = dioOutArgs(table, rdTickList(table, 8));
  if (o.why) { showMessage(o.why, true); return; }
  await rdSet("dout", o.args);
}

async function dioOut(block) {
  if (rdLive("dout")) { await rdEnd("dout"); renderRight(); return; }
  if (memoBlocked()) { return; }
  const chs = rdTickList(block, 8);
  if (!chs.length) { showMessage(T("rd.tick_first"), true); return; }
  const o = dioOutArgs(block, chs);
  if (o.why) { showMessage(o.why, true); return; }
  const args = o.args;
  rdBusy = true; renderRight();
  try {
    rdDoBlock = block;
    const why = await rdStart("dout", [RD_DO_ITEM[block]]);
    if (why) { rdDoBlock = ""; showMessage(why, true); return; }
    // Refused by the board: nothing was output, so no row is judged.
    if (!await rdSet("dout", args)) { rdDoBlock = ""; await rdEnd("dout"); }
  } finally {
    rdBusy = false;
    libRefresh();
  }
}

// The board's side of a DO row: the duty it reports and whether it is on.
function dioDoState(lv, block, ch) {
  if (!lv || rdDoBlock !== block || !lv.duty) { return "-"; }
  const d = lv.duty[ch - 1];
  return T(d > 0 ? "lib.live.on" : "lib.live.off") + " (" + d + ")";
}

/* ---------------- DO 带负载输出 ---------------- */

function dioDropMax() {
  const it = dioItem("dout.load");
  const cr = ((it && it.criteria) || []).find(c => c.step === "high-side-drop");
  const c = cr && (cr.checks || [])[0];
  return c && typeof c.max === "number" ? c.max : undefined;
}

function dioDoLoad(lv) {
  const max = dioDropMax();
  const sec = dioHead("1", T("dio.blk.load"), "drop");
  sec.dataset.dioblk = "drop";
  sec.appendChild(el("div", "hint", T("dio.load_hint_live")));
  sec.appendChild(dioParam("dout.load", "period"));
  const rows = rdDoBlock === "drop" && lv ? (lv.blocks || {}).drop : null;
  const tbl = dioTable(["", T("lib.live.col.ch"), T("dio.col.board_out"), T("dio.col.std_drop"),
    max === undefined ? T("dio.col.result") : T("dio.col.result_max", { max })]);
  for (let ch = 1; ch <= 8; ch++) {
    const key = "load|" + ch, tr = el("tr"); tr.dataset.ch = String(ch);
    tr.appendChild(el("td")).appendChild(rdTickBox("drop", ch));
    tr.appendChild(el("td", "mono", "DO" + ch));
    tr.appendChild(el("td", "mono", dioDoState(lv, "drop", ch)));
    const v = el("input"); v.className = "pt"; v.type = "number"; v.step = "any"; v.dataset.diostd = key;
    tr.appendChild(el("td")).appendChild(memoCell(v, "drop|" + ch, () => dioStd[key], x => { dioStd[key] = x; }));
    v.onchange = () => { if (!v.classList.contains("badcell") && rdDoBlock === "drop") { rdRef("dout", "drop", ch, v.value); } };
    tr.appendChild(dioCmp(rows, ch, DIO_JUDGE));
    tbl.appendChild(tr);
  }
  sec.appendChild(tbl);
  dioResult(sec, "dout.load");
  return sec;
}

/* ---------------- DO 翻转计数 ---------------- */

function dioDoBlink(lv) {
  const sec = dioHead("2", T("dio.blk.blink"), "blink");
  sec.dataset.dioblk = "blink";
  sec.appendChild(el("div", "hint", T("dio.blink_hint_live")));
  sec.appendChild(dioParam("dout.blink", "period"));
  const mine = rdDoBlock === "blink" && lv;
  if (mine) {
    const n = el("div", "hint", T("dio.blink_now", { n: lv.blink || 0, of: lv.blink_of || 0 }));
    n.dataset.blinknow = String(lv.blink || 0);
    sec.appendChild(n);
  }
  const rows = mine ? (lv.blocks || {}).blink : null;
  const tbl = dioTable(["", T("lib.live.col.ch"), T("dio.col.board_out"), T("dio.col.std_count"), T("dio.col.result")]);
  for (let ch = 1; ch <= 8; ch++) {
    const key = "blink|" + ch, tr = el("tr"); tr.dataset.ch = String(ch);
    tr.appendChild(el("td")).appendChild(rdTickBox("blink", ch));
    tr.appendChild(el("td", "mono", "DO" + ch));
    tr.appendChild(el("td", "mono", dioDoState(lv, "blink", ch)));
    const v = el("input"); v.className = "pt"; v.type = "number"; v.step = "1"; v.dataset.diostd = key;
    tr.appendChild(el("td")).appendChild(memoCell(v, "blk|" + ch, () => dioStd[key], x => { dioStd[key] = x; }));
    v.onchange = () => { if (!v.classList.contains("badcell") && rdDoBlock === "blink") { rdRef("dout", "blink", ch, v.value); } };
    tr.appendChild(dioCmp(rows, ch, DIO_JUDGE));
    tbl.appendChild(tr);
  }
  sec.appendChild(tbl);
  dioResult(sec, "dout.blink");
  return sec;
}

/* ---------------- DO PWM 方波 ---------------- */

function dioPwmRow(ch) { return dioPwm.rows[ch] = dioPwm.rows[ch] || { on: ch === 1, freq: "1000", duty: "128" }; }

// Checked on the page before anything is sent: duty fits the resolution and
// the frequency fits the timer (porttool_dout.c: up to 256000 >> res Hz).
function dioPwmProblem() {
  const res = Number(dioPwm.res), maxDuty = Math.pow(2, res) - 1, maxF = Math.floor(256000 / Math.pow(2, res));
  const on = Object.keys(dioPwm.rows).map(Number).filter(ch => dioPwm.rows[ch].on).sort();
  if (!on.length) { return T("dio.pwm_none"); }
  for (const ch of on) {
    const r = dioPwm.rows[ch], d = Number(r.duty), f = Number(r.freq);
    if (!Number.isInteger(d) || d < 0 || d > maxDuty) { return T("dio.pwm_duty_bad", { ch: "DO" + ch, max: maxDuty, res }); }
    if (!Number.isInteger(f) || f < 1 || f > maxF) { return T("dio.pwm_freq_bad", { ch: "DO" + ch, max: maxF, res }); }
  }
  return "";
}

function dioDoPwm(lv) {
  for (let ch = 1; ch <= 8; ch++) { dioPwmRow(ch); }
  const sec = dioHead("3", T("dio.blk.pwm"), "pwm");
  sec.dataset.dioblk = "pwm";
  sec.appendChild(el("div", "hint", T("dio.pwm_hint_live")));
  const rr = el("label", "row", T("dio.pwm_res") + " ");
  const rs = el("select"); rs.dataset.diores = "1";
  for (const r of ["8", "10", "12", "16"]) { const o = new Option(T("dio.bits", { n: r }), r); o.selected = dioPwm.res === r; rs.appendChild(o); }
  rs.onchange = () => { dioPwm.res = rs.value; renderRight(); };
  rr.appendChild(rs);
  rr.appendChild(el("span", "hint", " " + T("dio.pwm_res_hint")));
  sec.appendChild(rr);
  const rows = rdDoBlock === "pwm" && lv ? (lv.blocks || {}).pwm : null;
  const tbl = dioTable(["", T("lib.live.col.ch"), T("dio.col.freq"), T("dio.col.duty"), T("dio.col.pct"), T("dio.col.scope"), T("dio.col.result")]);
  const full = Math.pow(2, Number(dioPwm.res)) - 1;
  for (let ch = 1; ch <= 8; ch++) {
    const r = dioPwmRow(ch), tr = el("tr"); tr.dataset.ch = String(ch);
    const cb = rdTickBox("pwm", ch); cb.dataset.diopwm = String(ch);
    tr.appendChild(el("td")).appendChild(cb);
    tr.appendChild(el("td", "mono", "DO" + ch));
    for (const k of ["freq", "duty"]) {
      const inp = el("input"); inp.className = "pt"; inp.type = "number"; inp.step = "1"; inp.dataset["diopwm" + k] = String(ch);
      tr.appendChild(el("td")).appendChild(memoCell(inp, "pwm|" + ch + "|" + k, () => r[k], x => { r[k] = x; }));
      inp.onchange = () => { if (!inp.classList.contains("badcell")) { renderRight(); } };
    }
    const d = Number(r.duty);
    tr.appendChild(el("td", "mono", isFinite(d) && full > 0 ? (d / full * 100).toFixed(1) + " %" : "-"));
    const key = "pwmok|" + ch;
    const look = el("select"); look.dataset.diostd = key; look.dataset.liveref = key;
    look.appendChild(new Option(T("lib.live.ref_unset"), ""));
    look.appendChild(new Option(T("dio.scope.ok"), "1"));
    look.appendChild(new Option(T("dio.scope.bad"), "0"));
    look.value = dioStd[key] ?? "";
    look.onchange = () => { dioStd[key] = look.value; look.blur(); if (rdDoBlock === "pwm") { rdRef("dout", "pwm", ch, look.value); } };
    tr.appendChild(el("td")).appendChild(look);
    tr.appendChild(dioCmp(rows, ch, DIO_MATCH));
    tbl.appendChild(tr);
  }
  sec.appendChild(tbl);
  const why = dioPwmProblem();
  if (why) { const w = el("div", "chk bad", why); w.dataset.diopwmbad = "1"; sec.appendChild(w); }
  dioResult(sec, "dout.square");
  return sec;
}
