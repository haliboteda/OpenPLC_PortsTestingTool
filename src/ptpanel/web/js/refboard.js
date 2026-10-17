"use strict";

// The reference board's settings page (decision 135, PORTTOOL-FLOW.md C.3.6
// 「标准机设置页」): analog, digital and other ports on top, one save bar, then
// the notes and what the settings leave the fixture tests able to test. The
// impact is worked out by the server from the unsaved settings
// (/api/refboard/check), the same function the full-test gate uses.

let refPage = null;    // {state, draft: {wiring, ranges}, msg, problems, impact}
let refCheckTimer = 0;
let refStatusTimer = 0;

const REF_ANALOG = ["AO1", "AO2", "AI1", "AI2"];
const REF_BAUDS = [9600, 19200, 38400, 57600, 115200, 230400];
const REF_CAN_BAUDS = [125000, 250000, 500000, 1000000];

function refDraftOf(st) {
  const ranges = {};
  for (const ch of REF_ANALOG) { ranges[ch] = ((st.ranges || {})[ch] || {}).range; }
  return { wiring: JSON.parse(JSON.stringify(st.wiring || {})), ranges };
}

async function openRefPage() {
  const st = await api("/api/refboard");
  if (st.error) { showMessage(st.error, true); return; }
  refPage = { state: st, draft: refDraftOf(st), msg: null, problems: [], impact: st.impact || [], status: null };
  renderRefPage();
  clearInterval(refStatusTimer);
  refStatusTimer = setInterval(refStatusTick, 1000);
  refStatusTick();
}

function closeRefPage() {
  clearInterval(refStatusTimer);
  refPage = null;
  const box = document.getElementById("refpage");
  if (box) { box.remove(); }
}

// Every change: work the problems and the impact out again, not saved.
function refChanged() {
  renderRefPage();
  clearTimeout(refCheckTimer);
  refCheckTimer = setTimeout(async () => {
    if (!refPage) { return; }
    const j = await api("/api/refboard/check", refPage.draft);
    if (!refPage || j.error) { return; }
    refPage.problems = j.problems || [];
    refPage.impact = j.impact || [];
    renderRefPage();
  }, 250);
}

function refSelect(opts, cur, on, key) {
  const s = el("select");
  if (key) { s.dataset.ref = key; }
  for (const [v, label] of opts) {
    const o = el("option", null, label); o.value = String(v); o.selected = String(v) === String(cur); s.appendChild(o);
  }
  s.onchange = () => { on(s.value); refChanged(); };
  return s;
}

function refNum(cur, on, key, min) {
  const i = el("input"); i.type = "number"; i.value = cur; i.size = 5; i.className = "pt"; i.dataset.ref = key;
  if (min !== undefined) { i.min = String(min); }
  i.onchange = () => { on(Number(i.value)); refChanged(); };
  return i;
}

const refPill = (cls, text) => el("span", "pill " + cls, text);

function renderRefPage() {
  if (!refPage) { return; }
  let box = document.getElementById("refpage");
  if (!box) { box = el("div"); box.id = "refpage"; document.body.appendChild(box); }
  box.textContent = "";
  const st = refPage.state, w = refPage.draft.wiring, p = w.params;
  const hd = el("div", "rphead");
  hd.appendChild(el("b", null, T("refpg.title")));
  hd.appendChild(el("span", "tag", "UID " + (st.uid || "-")));
  hd.appendChild(el("span", "tag", (st.port || "") + (st.firmware ? " · " + st.firmware : "")));
  hd.appendChild(el("span", "sp"));
  hd.appendChild(el("span", "hint", T("refpg.by_uid")));
  const close = el("button", null, T("refpg.close")); close.dataset.refclose = "1"; close.onclick = closeRefPage;
  hd.appendChild(close);
  box.appendChild(hd);
  box.appendChild(refAnalogCard(st, w, p));
  box.appendChild(refDigitalCard(w, p));
  box.appendChild(refPortsCard(st, p));
  box.appendChild(refSaveBar());
  box.appendChild(refNotes());
  box.appendChild(refImpactCard());
  refPaintStatus();
}

/* ---------------- 模拟量 ---------------- */

function refAnalogCard(st, w, p) {
  const card = el("div", "rpcard");
  card.appendChild(el("h3", null, T("refpg.analog")));
  const tbl = el("table", "gtab");
  const hr = el("tr");
  for (const k of ["refpg.col.refch", "ana.range", "refpg.col.to_dut", "refpg.col.cal", "refpg.col.state"]) { hr.appendChild(el("th", null, T(k))); }
  tbl.appendChild(hr);
  const used = {};
  for (const ch of REF_ANALOG) { const to = w.analog[ch]; if (to) { used[to] = (used[to] || 0) + 1; } }
  for (const ch of REF_ANALOG) {
    const tr = el("tr"); tr.dataset.refana = ch;
    tr.appendChild(el("td", "mono", ch));
    const opts = (((st.ranges || {})[ch] || {}).options || []).map(r => [r, anaRangeName(r)]);
    tr.appendChild(el("td")).appendChild(refSelect(opts, refPage.draft.ranges[ch], v => { refPage.draft.ranges[ch] = v; }, "range|" + ch));
    const targets = ch.startsWith("AO") ? ["AI1", "AI2"] : ["AO1", "AO2"];
    const to = w.analog[ch] || "";
    tr.appendChild(el("td")).appendChild(refSelect([["", T("refpg.none")]].concat(targets.map(t => [t, t])), to, v => { w.analog[ch] = v; }, "to|" + ch));
    const kb = ((st.cal || {}).ch || {})[ch], burnt = (st.burnt || {})[ch];
    const cal = el("td");
    if (kb) {
      cal.appendChild(el("span", "mono", "k = " + Number(kb.k).toFixed(4) + "  b = " + Number(kb.b).toFixed(4)));
      if (burnt) { cal.appendChild(el("div", "hint", T("refpg.burnt_as", { r: anaRangeName(burnt) }))); }
    } else {
      cal.appendChild(refPill("fail", T("refpg.no_cal")));
    }
    tr.appendChild(cal);
    tr.appendChild(el("td")).appendChild(refAnalogState(ch, to, used[to] > 1, kb, burnt));
    // A click on the row opens this channel's range block below it (decision 136).
    const open = refPage.open === ch;
    tr.className = "rpopen" + (open ? " on" : "");
    tr.onclick = e => {
      if (e.target.closest("select,input,button")) { return; }
      refPage.open = open ? "" : ch;
      renderRefPage();
    };
    tbl.appendChild(tr);
    if (open) {
      const xr = el("tr"); xr.dataset.refrange = ch;
      const td = el("td"); td.colSpan = 5;
      td.appendChild(rngBlock(ch, (st.ranges || {})[ch], { range: refPage.draft.ranges[ch], select: null,
        kb: kb || null, calRange: burnt || "", why: T("refpg.no_cal") }));
      xr.appendChild(td);
      tbl.appendChild(xr);
    }
  }
  card.appendChild(tbl);
  // The board under test's range is not compared here: wiring keeps both ends alike (decision 140).
  card.appendChild(el("div", "hint", T("refpg.analog_same_kind")));
  card.appendChild(el("div", "hint", T("rng.ref_open")));
  const prm = el("div", "row rpparams");
  prm.appendChild(el("label", null, T("refpg.ai_period") + " ")).appendChild(refNum(p.ai_period_ms, v => { p.ai_period_ms = v; }, "ai_period_ms", 1));
  prm.appendChild(el("label", null, T("refpg.ai_frames") + " ")).appendChild(refNum(p.ai_frames, v => { p.ai_frames = v; }, "ai_frames", 1));
  card.appendChild(prm);
  return card;
}

// One analog row's state, the first problem in the order PORTTOOL-FLOW.md
// C.3.6 「模拟量」 lists them; the calibration range compares the exact range
// (0-20 mA vs 4-20 mA differs).
function refAnalogState(ch, to, twice, kb, burnt) {
  const r = refPage.draft.ranges[ch];
  let p;
  if (!to) { p = refPill("", T("refpg.unwired")); }
  else if (twice) { p = refPill("fail", T("refpg.twice", { ch: to })); }
  else if (!kb) { p = refPill("fail", T("refpg.no_cal")); }
  else if (burnt && burnt !== r) { p = refPill("fail", T("rng.kb_other_range", { r: anaRangeName(burnt) })); }
  else { p = refPill("pass", T("refpg.matched")); }
  p.dataset.refstate = ch;
  return p;
}

/* ---------------- 数字量 ---------------- */

function refDigitalCard(w, p) {
  const card = el("div", "rpcard");
  card.appendChild(el("h3", null, T("refpg.digital")));
  const tbl = el("table", "gtab");
  const hr = el("tr");
  for (const k of ["refpg.col.dutch", "refpg.col.via", "refpg.col.to_ref", "refpg.col.now", "refpg.col.state"]) { hr.appendChild(el("th", null, T(k))); }
  tbl.appendChild(hr);
  const relays = {};
  for (const side of ["do", "di"]) {
    for (let n = 1; n <= 8; n++) { const l = (w[side] || {})[n]; if (l && l.via) { relays[l.via] = (relays[l.via] || 0) + 1; } }
  }
  for (const side of ["do", "di"]) {
    const name = side === "do" ? "DO" : "DI", refName = side === "do" ? "DI" : "DO";
    const refUse = {};
    for (let n = 1; n <= 8; n++) { const l = w[side][n]; if (l && l.to) { refUse[l.to] = (refUse[l.to] || 0) + 1; } }
    for (let n = 1; n <= 8; n++) {
      const l = w[side][n] = w[side][n] || { via: 0, to: 0 };
      const tr = el("tr"); tr.dataset.refdig = name + n;
      tr.appendChild(el("td", "mono", name + n + (side === "do" ? " →" : " ←")));
      const via = [[0, T("refpg.direct")]].concat([1, 2, 3, 4, 5, 6].map(k => [k, T("refpg.relay", { n: k })]));
      tr.appendChild(el("td")).appendChild(refSelect(via, l.via, v => { l.via = Number(v); }, "via|" + name + n));
      const to = [[0, T("refpg.none")]].concat([1, 2, 3, 4, 5, 6, 7, 8].map(k => [k, refName + k]));
      tr.appendChild(el("td")).appendChild(refSelect(to, l.to, v => { l.to = Number(v); }, "to|" + name + n));
      const now = el("td"); now.dataset.refnow = name + n;
      tr.appendChild(now);
      const stc = el("td");
      if (!l.to) { stc.appendChild(refPill("", T("refpg.unwired"))); }
      else if (refUse[l.to] > 1) { stc.appendChild(refPill("fail", T("refpg.twice", { ch: refName + l.to }))); }
      else if (l.via && relays[l.via] > 1) { stc.appendChild(refPill("fail", T("refpg.relay_twice", { n: l.via }))); }
      else { stc.appendChild(refPill("pass", T("refpg.matched"))); }
      tr.appendChild(stc);
      tbl.appendChild(tr);
    }
  }
  card.appendChild(tbl);
  const onDO = [1, 2, 3, 4, 5, 6].filter(k => Object.values(w.do).some(l => l.via === k));
  const miss = [1, 2, 3, 4, 5, 6].filter(k => !onDO.includes(k));
  const note = el("div", miss.length ? "chk bad" : "chk", miss.length ? T("refpg.relays_unchained", { list: miss.map(k => "RY" + k).join(T("shell.list_sep")) }) : T("refpg.relays_all"));
  note.dataset.refrelays = "1";
  card.appendChild(note);
  const prm = el("div", "row rpparams");
  prm.appendChild(el("label", null, T("refpg.di_period") + " ")).appendChild(refNum(p.di_period_ms, v => { p.di_period_ms = v; }, "di_period_ms", 50));
  prm.appendChild(el("label", null, T("refpg.di_settle") + " ")).appendChild(refNum(p.di_settle_frames, v => { p.di_settle_frames = v; }, "di_settle_frames", 1));
  prm.appendChild(el("label", null, T("refpg.do_hold") + " ")).appendChild(refNum(p.do_hold_ms, v => { p.do_hold_ms = v; }, "do_hold_ms", 0));
  card.appendChild(prm);
  return card;
}

/* ---------------- 其余端口 ---------------- */

function refPortsCard(st, p) {
  const card = el("div", "rpcard");
  card.appendChild(el("h3", null, T("refpg.ports")));
  const tbl = el("table", "gtab");
  const hr = el("tr");
  for (const k of ["refpg.col.port", "refpg.col.param", "refpg.col.value", "refpg.col.now", "refpg.col.in_test"]) { hr.appendChild(el("th", null, T(k))); }
  tbl.appendChild(hr);
  const now = (port, k) => ((st.ports || {})[port] || {})[k] || "—";
  const row = (port, param, value, cur, use) => {
    const tr = el("tr");
    tr.appendChild(el("td", null, port));
    tr.appendChild(el("td", null, param));
    const v = el("td"); if (typeof value === "string") { v.textContent = value; v.className = "hint"; } else { v.appendChild(value); }
    tr.appendChild(v);
    tr.appendChild(el("td", "mono", cur));
    tr.appendChild(el("td", "hint", use));
    tbl.appendChild(tr);
  };
  row(T("refpg.p.board"), T("refpg.p.uid_fw"), (st.uid || "-") + " · " + (st.firmware || "-"), T("refpg.p.connected"), "—");
  // Refreshed every second (refPaintStatus): the control line, the network link, the CAN bus.
  for (const [k, port, param] of [["ctrl", "RS232", "refpg.p.ctrl"], ["eth", T("refpg.p.eth"), "refpg.p.link"], ["can", "CAN", "refpg.p.bus"]]) {
    row(port, T(param), T("refpg.p.not_set"), "", "—");
    tbl.lastChild.children[3].dataset.refstat = k;
  }
  row("RS232", T("refpg.p.baud"), refSelect(REF_BAUDS.map(b => [b, String(b)]), p.rs232_baud, v => { p.rs232_baud = Number(v); }, "rs232_baud"),
      now("rs232", "baud"), T("refpg.use.rs232"));
  const ip = el("input"); ip.value = p.eth_ip === "dhcp" ? "" : p.eth_ip; ip.placeholder = "DHCP"; ip.size = 14; ip.dataset.ref = "eth_ip";
  ip.onchange = () => { p.eth_ip = ip.value.trim() === "" ? "dhcp" : ip.value.trim(); refChanged(); };
  row(T("refpg.p.eth"), T("refpg.p.ip"), ip, now("eth", "ip"), T("refpg.use.none"));
  row(T("refpg.p.eth"), T("refpg.p.mask_gw"), T("refpg.p.mask_gw_fixed"), "—", T("refpg.use.none"));
  row("CAN", T("refpg.p.baud"), refSelect(REF_CAN_BAUDS.map(b => [b, b / 1000 + " kbit/s"]), p.can_baud, v => { p.can_baud = Number(v); }, "can_baud"),
      now("can", "baud"), T("refpg.use.can"));
  row("CAN", T("refpg.p.can_id"), T("refpg.p.can_id_fixed"), "0x123", T("refpg.use.can"));
  row("RS485", T("refpg.p.baud"), refSelect(REF_BAUDS.map(b => [b, String(b)]), p.rs485_baud, v => { p.rs485_baud = Number(v); }, "rs485_baud"),
      now("rs485", "baud"), T("refpg.use.none"));
  row("RS485", T("refpg.p.period"), refNum(p.rs485_period_ms, v => { p.rs485_period_ms = v; }, "rs485_period_ms", 1), now("rs485", "period"), T("refpg.use.none"));
  row(T("refpg.p.temp"), "—", T("refpg.p.not_set"), "", T("refpg.use.none"));
  tbl.lastChild.children[3].dataset.reftemp = "1";
  row(T("refpg.p.others"), "—", T("refpg.p.not_set"), T("refpg.p.not_used"), T("refpg.use.none"));
  card.appendChild(tbl);
  return card;
}

/* ---------------- 保存、注意事项、影响 ---------------- */

function refSaveBar() {
  const bar = el("div", "rpsave");
  const save = el("button", "primary", T("refpg.save")); save.dataset.refsave = "1";
  save.onclick = async () => {
    const j = await api("/api/refboard", refPage.draft);
    if (j.error && !j.uid) { refPage.msg = ["fail", j.error]; renderRefPage(); return; }
    refPage.state = j.error ? refPage.state : j;
    refPage.problems = j.problems || [];
    refPage.msg = j.error ? ["fail", j.error] : ["pass", T("refpg.saved")];
    if (!j.error) { refPage.draft = refDraftOf(j); refPage.impact = j.impact || []; await loadStageCfgs(); }
    renderRefPage();
  };
  const back = el("button", null, T("refpg.restore")); back.dataset.refrestore = "1";
  back.onclick = () => { refPage.draft = refDraftOf(refPage.state); refPage.msg = null; refChanged(); };
  bar.appendChild(save); bar.appendChild(back);
  if (refPage.msg) { const m = refPill(refPage.msg[0], refPage.msg[1]); m.dataset.refmsg = refPage.msg[0]; bar.appendChild(m); }
  bar.appendChild(el("span", "sp"));
  bar.appendChild(el("span", "hint", T("refpg.save_all")));
  for (const p of refPage.problems || []) { const d = el("div", "chk bad", p); d.dataset.refproblem = "1"; bar.appendChild(d); }
  return bar;
}

function refNotes() {
  const card = el("div", "rpcard");
  card.appendChild(el("h3", null, T("refpg.notes")));
  const ul = el("ul");
  for (const k of ["refpg.note.only_fixture", "refpg.note.digital", "refpg.note.analog", "refpg.note.cal", "refpg.note.gnd", "refpg.note.refuse"]) { ul.appendChild(el("li", null, T(k))); }
  card.appendChild(ul);
  return card;
}

function refImpactCard() {
  const card = el("div", "rpcard");
  card.appendChild(el("h3", null, T("refpg.impact")));
  const tbl = el("table", "gtab"); tbl.dataset.refimpact = "1";
  const hr = el("tr");
  for (const k of ["refpg.col.item", "refpg.col.by", "refpg.col.can"]) { hr.appendChild(el("th", null, T(k))); }
  tbl.appendChild(hr);
  for (const r of refPage.impact || []) {
    const tr = el("tr"); tr.dataset.impact = r.item + "|" + r.ch;
    tr.appendChild(el("td", null, r.name + " · " + r.ch));
    tr.appendChild(el("td", "mono", r.ref ? r.ch + " ← " + r.ref : "—"));
    tr.appendChild(el("td")).appendChild(refPill(r.ok ? "pass" : "fail", r.ok ? T("refpg.can") : r.why));
    tbl.appendChild(tr);
  }
  card.appendChild(tbl);
  return card;
}

/* ---------------- 状态，每秒 ---------------- */

// Every second while the page is open and nothing else runs: the reference
// DI levels and temperatures (/api/refboard/status). Only those cells are
// rewritten, so an open dropdown stays open (decision 135).
async function refStatusTick() {
  if (!refPage) { return; }
  const j = await api("/api/refboard/status");
  if (!refPage) { return; }
  refPage.status = j;
  refPaintStatus();
}

function refPaintStatus() {
  const st = (refPage && refPage.status) || {}, w = refPage ? refPage.draft.wiring : {};
  const level = b => T(b ? "lib.live.level.hi" : "lib.live.level.lo");
  for (const td of document.querySelectorAll("#refpage [data-refnow]")) {
    const name = td.dataset.refnow, n = Number(name.slice(2)), l = (w[name.slice(0, 2).toLowerCase()] || {})[n] || {};
    if (st.busy) { td.textContent = T("refpg.busy"); continue; }
    if (name.startsWith("DI")) { td.textContent = T("refpg.do_idle"); continue; }   // the reference DOs are not driven here
    td.textContent = l.to && st.din ? level(st.din[l.to - 1]) : "—";
  }
  const stat = { ctrl: () => T(st.ctrl ? "refpg.s.ctrl_ok" : "refpg.s.ctrl_bad"),
    eth: () => !st.eth ? "—" : st.eth.link === "1" ? T("refpg.s.link_up", { speed: st.eth.speed || "?", duplex: T(st.eth.fd === "1" ? "refpg.s.full" : "refpg.s.half") }) : T("refpg.s.link_down"),
    can: () => !st.can ? "—" : T("refpg.s.can", { tec: st.can.tec || "0", rec: st.can.rec || "0", lec: st.can.lec || "-", bus: st.can.bus || "-" }) };
  for (const td of document.querySelectorAll("#refpage [data-refstat]")) {
    td.textContent = st.busy ? T("refpg.busy") : stat[td.dataset.refstat]();
  }
  const t = document.querySelector("#refpage [data-reftemp]");
  if (t) {
    t.textContent = st.busy ? T("refpg.busy")
      : (st.temp || []).map((v, i) => T("lib.chan.temp", { n: i + 1 }) + " " + (v ? v + " °C" : "—")).join(T("shell.list_sep")) || "—";
  }
}
