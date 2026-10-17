"use strict";

// Live cards, a person standing in for the reference board: one row per channel, board
// side, typed reference and their deviation (decision 108, PORTTOOL-FLOW.md C.3.6 "实时卡片").
// The server keeps the session and judges; this draws and forwards to /api/lib/live.

const liveMA = {};       // `${item}|${ch}` -> mA typed for an AO, kept across redraws
const liveRefTyped = {}; // `${item}|${ch}` -> reference value as typed

async function liveAct(id, body) {
  const j = await api("/api/lib/live", Object.assign({ id }, body));
  if (j.error) { showMessage(j.error, true); return; }
  libRefresh();
}

// True while a dropdown on the cards is open: a redraw would close it.
const liveChoosing = () => { const a = document.activeElement;
  return !!(a && a.tagName === "SELECT" && a.closest && a.closest("#itemcards")); };

// The data-* keys that name a typed field on the cards, so a redraw can find it again.
const LIVE_FIELD_KEYS = ["livema", "liveref", "anatarget", "anaactual", "anapt", "anagot", "diostd", "diopwmfreq", "diopwmduty", "libparam"];

// Redraws without losing what is being typed on a live card: the field with
// the same key gets the text and the focus back.
function liveKeepFocus(draw) {
  const a = document.activeElement, d = (a && a.dataset) || {};
  const k = LIVE_FIELD_KEYS.find(x => d[x]);
  const sel = k ? "[data-" + k + '="' + d[k] + '"]' : null;
  const val = a && a.value, pos = a && a.type !== "number" ? a.selectionStart : null;
  draw();
  if (!sel) { return; }
  const n = document.querySelector(sel);
  if (!n) { return; }
  n.value = val;
  n.focus();
  if (pos !== null && pos !== undefined && n.setSelectionRange) { n.setSelectionRange(pos, pos); }
}

// The reference field of one channel: a select for a level, a number otherwise.
function liveRefField(it, ch, kind, running) {
  const key = it.id + "|" + ch;
  let f;
  if (kind === "din" || kind === "dout") {
    f = el("select");
    const [hi, lo] = kind === "din" ? ["lib.live.level.hi", "lib.live.level.lo"] : ["lib.live.do.has", "lib.live.do.none"];
    f.appendChild(new Option(T("lib.live.ref_unset"), ""));
    f.appendChild(new Option(T(hi), "1"));
    f.appendChild(new Option(T(lo), "0"));
  } else {
    f = el("input"); f.type = "number"; f.step = "any";
  }
  f.dataset.liveref = key;
  f.value = liveRefTyped[key] ?? "";
  f.disabled = !running;
  // The quadrature step count is a typed cell: checked, kept only when good (memo.js).
  if (kind === "quad") {
    const w = memoCell(f, "quad|" + ch, () => liveRefTyped[key], x => { liveRefTyped[key] = x; });
    f.onchange = () => { if (!f.classList.contains("badcell")) { liveAct(it.id, { op: "ref", ch, value: f.value }); } };
    return w;
  }
  f.oninput = () => { liveRefTyped[key] = f.value; };
  f.onchange = () => {
    liveRefTyped[key] = f.value;
    if (f.tagName === "SELECT") { f.blur(); }
    liveAct(it.id, { op: "ref", ch, value: f.value });
  };
  return f;
}

// The last cell of a row: matched or not for levels and counts, the
// deviation in % FS (marked against the plan's criterion) for AI / AO.
function liveCmpCell(lv, ch) {
  const r = (lv.ref || []).find(x => x.ch === ch);
  const td = el("td", "mono");
  td.dataset.cmp = String(ch);
  if (!r) { td.textContent = "-"; return td; }
  if (r.board === undefined || r.board === null) { td.textContent = T("lib.live.no_board_short"); return td; }
  const analog = lv.kind === "ain" || lv.kind === "aout";
  let text = analog ? (r.dev === undefined || r.dev === null ? "-" : r.dev.toFixed(2) + " % FS") : "";
  if (r.pass === true || r.pass === false) {
    const word = analog ? (r.pass ? "lib.live.pass" : "lib.live.fail") : (r.pass ? "lib.live.match" : "lib.live.mismatch");
    text = (r.pass ? "✅ " : "❌ ") + (text ? text + " " : "") + T(word);
    td.className = "mono " + (r.pass ? "ok" : "bad");
  }
  td.dataset.pass = r.pass === true ? "1" : r.pass === false ? "0" : "";
  td.textContent = text;
  return td;
}

function liveTable(heads) {
  const tbl = el("table", "quad live"), tr = el("tr");
  for (const h of heads) { tr.appendChild(el("th", null, h)); }
  tbl.appendChild(tr);
  return tbl;
}

function cell(child, cls) {
  const td = el("td", cls || null);
  if (typeof child === "string") { td.textContent = child; } else if (child) { td.appendChild(child); }
  return td;
}

// The criterion's tolerance for a live AI / AO column head, from the matching
// read / output item (the same plan step the server judges by).
function liveTol(kind, ch) {
  const id = (kind === "ain" ? "ain.read" : "aout.out") + ch;
  const it = (catalog.items || []).find(x => x.id === id);
  return it ? (anaLimit(it).check || {}).max : undefined;
}

function libLive(it, st) {
  if (st.live.kind === "can") { return canWinCard(it, st); }
  if (st.live.kind === "knx") { return knxWinCard(it, st); }
  const lv = st.live, box = el("div", "live"), run = st.running;
  box.dataset.live = it.id;
  box.dataset.frames = String(lv.frames);
  if (run) { box.appendChild(el("div", "hint", T("lib.live.frames", { n: lv.frames }))); }
  const refHead = T("lib.live.col.ref"), cmpHead = T("lib.live.col.match");

  if (lv.kind === "din") {
    box.appendChild(el("div", "hint", T("lib.live.di_hint")));
    const tbl = liveTable([T("lib.live.col.ch"), T("lib.live.col.state"), refHead, cmpHead]);
    (lv.bits || []).forEach((on, i) => {
      const tr = el("tr");
      tr.dataset.ch = String(i + 1);
      const lamp = el("span", on ? "lamp on" : "lamp", T(on ? "lib.live.level.hi" : "lib.live.level.lo"));
      lamp.dataset.di = String(i + 1);
      lamp.dataset.on = on ? "1" : "0";
      tr.appendChild(cell("DI" + (i + 1), "mono"));
      tr.appendChild(cell(lamp));
      tr.appendChild(cell(liveRefField(it, i + 1, "din", run)));
      tr.appendChild(liveCmpCell(lv, i + 1));
      tbl.appendChild(tr);
    });
    box.appendChild(tbl);
  }

  if (lv.kind === "quad") {
    box.appendChild(el("div", "hint", T("lib.live.quad_hint")));
    const tbl = liveTable([T("lib.quad.col.group"), T("lib.quad.col.pins"), T("lib.live.col.count"),
      T("lib.live.col.dir"), T("lib.live.ref_quad_head"), cmpHead]);
    (lv.groups || []).forEach((g, i) => {
      const tr = el("tr");
      tr.dataset.group = String(i + 1);
      const dir = g.dir > 0 ? "lib.live.dir.up" : g.dir < 0 ? "lib.live.dir.down" : "lib.live.dir.still";
      for (const v of [String(i + 1), "DI" + (2 * i + 1) + " / DI" + (2 * i + 2), String(g.count), T(dir)]) {
        tr.appendChild(cell(v, "mono"));
      }
      tr.appendChild(cell(liveRefField(it, i + 1, "quad", run)));
      tr.appendChild(liveCmpCell(lv, i + 1));
      tbl.appendChild(tr);
    });
    box.appendChild(tbl);
    box.appendChild(el("div", "chk " + (lv.err ? "bad" : "ok"), T("lib.quad.err", { n: lv.err })));
    if (run) {
      const r = el("button", null, T("lib.live.reset"));
      r.dataset.livereset = it.id;
      r.onclick = () => liveAct(it.id, { op: "reset" });
      box.appendChild(r);
    }
  }

  if (lv.kind === "dout") {
    box.appendChild(el("div", "hint", T("lib.live.do_hint")));
    const tbl = liveTable([T("lib.live.col.ch"), T("lib.live.col.raw_do"), T("lib.live.col.state"), refHead, cmpHead]);
    (lv.bits || []).forEach((on, i) => {
      const tr = el("tr");
      tr.dataset.ch = String(i + 1);
      const b = el("button", on ? "lamp on" : "lamp", T(on ? "lib.live.on" : "lib.live.off"));
      b.dataset.do = String(i + 1);
      b.disabled = !run;
      b.onclick = () => liveAct(it.id, { op: "do", ch: i + 1, on: !on });
      tr.appendChild(cell("DO" + (i + 1), "mono"));
      tr.appendChild(cell(String((lv.duty || [])[i] ?? "-"), "mono"));
      tr.appendChild(cell(b));
      tr.appendChild(cell(liveRefField(it, i + 1, "dout", run)));
      tr.appendChild(liveCmpCell(lv, i + 1));
      tbl.appendChild(tr);
    });
    box.appendChild(tbl);
  }

  if (lv.kind === "ain") {
    const unit = ((lv.analog || []).find(a => a.unit) || {}).unit || "";
    const tol = liveTol("ain", 1);
    const tbl = liveTable([T("lib.live.col.ch"), T("lib.live.col.raw_ai"), T("lib.live.col.conv", { u: unit }),
      T("lib.live.col.ref_unit", { u: unit }), tol === undefined ? T("ana.col.dev_pct") : T("ana.col.dev_lim", { tol })]);
    for (const a of lv.analog || []) {
      const tr = el("tr");
      tr.dataset.ai = String(a.ch);
      const raw = (a.raw !== undefined && a.raw !== null ? a.raw.toFixed(0) : "-") + " / " +
        (a.pin_mv !== undefined && a.pin_mv !== null ? a.pin_mv + " mV" : "-");
      const v = a.value !== undefined && a.value !== null ? a.value.toFixed(a.unit === "mV" ? 0 : 3) + " " + a.unit : T("lib.live.no_conv");
      tr.appendChild(cell("AI" + a.ch, "mono"));
      tr.appendChild(cell(raw, "mono"));
      tr.appendChild(cell(v, "mono"));
      tr.appendChild(cell(liveRefField(it, a.ch, "ain", run)));
      tr.appendChild(liveCmpCell(lv, a.ch));
      tbl.appendChild(tr);
    }
    box.appendChild(tbl);
    for (const a of lv.analog || []) {
      if (!a.unit) { box.appendChild(el("div", "hint", T("lib.live.no_range"))); break; }
      if (!a.calibrated) { box.appendChild(el("div", "hint", T("lib.live.not_cal", { ch: "AI" + a.ch }))); }
    }
  }

  if (lv.kind === "aout") {
    box.appendChild(el("div", "hint", T("lib.live.ao_hint")));
    const tol = liveTol("aout", 1);
    const tbl = liveTable([T("lib.live.col.ch"), T("lib.live.ma_label"), T("lib.live.col.raw_ao"),
      T("lib.live.col.cal_ao"), refHead,
      tol === undefined ? T("ana.col.dev_pct") : T("ana.col.dev_lim", { tol })]);
    for (const a of lv.analog || []) {
      const tr = el("tr");
      tr.dataset.ao = String(a.ch);
      const key = it.id + "|" + a.ch;
      const set = el("span");
      const inp = el("input");
      // Unit and limits follow this channel's registered range (PORTTOOL-FLOW.md C.3.6 「AO 手动」).
      inp.type = "number"; inp.step = "any";
      if (a.unit) { inp.min = String(a.min || 0); inp.max = String(a.fs); inp.placeholder = (a.min || 0) + "–" + a.fs; }
      inp.dataset.livema = key;
      inp.value = liveMA[key] ?? "";
      inp.disabled = !run;
      inp.oninput = () => { liveMA[key] = inp.value; };
      const go = el("button", null, T("lib.live.ao_set"));
      go.dataset.aoset = String(a.ch);
      go.disabled = !run;
      go.onclick = () => liveAct(it.id, { op: "ao", ch: a.ch, ma: Number(inp.value) });
      set.appendChild(inp);
      if (a.unit) { set.appendChild(el("span", "sub", " " + a.unit + " ")); }
      set.appendChild(go);
      const calv = el("span", null, a.set !== undefined && a.set !== null && a.value !== undefined && a.value !== null
        ? T("lib.live.ao_val", { set: a.set, v: a.value.toFixed(a.unit === "mV" ? 0 : 3), u: a.unit }) : T("lib.live.ao_unset"));
      calv.dataset.aoval = String(a.ch);
      tr.appendChild(cell("AO" + a.ch, "mono"));
      tr.appendChild(cell(set));
      tr.appendChild(cell(a.raw !== undefined && a.raw !== null ? String(a.raw) : "-", "mono"));
      tr.appendChild(cell(calv, "mono"));
      tr.appendChild(cell(liveRefField(it, a.ch, "aout", run)));
      tr.appendChild(liveCmpCell(lv, a.ch));
      tbl.appendChild(tr);
    }
    box.appendChild(tbl);
    for (const a of lv.analog || []) {
      if (!a.calibrated) { box.appendChild(el("div", "hint", T("lib.live.not_cal", { ch: "AO" + a.ch }))); }
    }
  }

  if (run) {
    const end = el("button", "primary", T("lib.live.end"));
    end.dataset.liveend = it.id;
    end.onclick = () => liveAct(it.id, { op: "end" });
    box.appendChild(end);
  }
  return box;
}

// DO under load measured by a person, one channel on at a time (STG-32):
// A din reading as words: which inputs are high.
function dinBitsText(v) {
  const bits = parseInt(v);   // "0x05" or "5"
  if (Number.isNaN(bits) || bits < 0) { return v; }
  if (bits === 0) { return T("lib.din.all_low"); }
  const hi = [];
  for (let i = 0; i < 8; i++) { if ((bits >> i) & 1) { hi.push("DI" + (i + 1)); } }
  return T("lib.din.high", { list: hi.join(T("shell.list_sep")) });
}

// DI level walked by a person in R&D (decisions 128, 133): per input what it should read,
// what the board read and why it ended.
function libDinWalk(d) {
  const box = el("div");
  box.dataset.dinwalk = d.on ? String(d.on) : "done";
  if (d.on) { box.appendChild(el("div", "hint", T("lib.din.on", { ch: d.on }))); }
  if (!(d.rows || []).length) { return box; }
  const tbl = liveTable([T("lib.live.col.ch"), T("lib.din.col.want"), T("dio.col.board_level"), T("dio.col.result")]);
  for (const r of d.rows) {
    const tr = el("tr");
    tr.dataset.ch = String(r.ch);
    tr.appendChild(cell("DI" + r.ch, "mono"));
    tr.appendChild(cell(T("lib.din.only", { ch: r.ch })));
    tr.appendChild(cell(r.seen ? dinBitsText(r.seen) : "-"));
    const why = T({ ok: "lib.din.why.ok", wrong: "lib.din.why.wrong", none: "lib.din.why.none" }[r.why] || "lib.din.why.none");
    const td = cell((r.pass ? "✅ " : "❌ ") + why, r.pass ? "ok" : "bad");
    td.dataset.pass = r.pass ? "1" : "0";
    tr.appendChild(td);
    tbl.appendChild(tr);
  }
  box.appendChild(tbl);
  return box;
}

// raw duty, state, the drop typed as the reference, and the criterion.
function libDrop(d) {
  const box = el("div");
  box.dataset.drop = d.on ? String(d.on) : "done";
  if (d.on) { box.appendChild(el("div", "hint", T("lib.drop.on", { ch: d.on }))); }
  if (!(d.rows || []).length) { return box; }
  const tbl = liveTable([T("lib.live.col.ch"), T("lib.live.col.raw_do"), T("lib.live.col.state"),
    T("lib.drop.col.ref"), T("lib.drop.col.crit", { max: d.max || "-" })]);
  for (const r of d.rows) {
    const tr = el("tr");
    tr.dataset.ch = String(r.ch);
    tr.appendChild(cell("DO" + r.ch, "mono"));
    tr.appendChild(cell(String(r.duty), "mono"));
    tr.appendChild(cell(T("lib.live.on")));
    tr.appendChild(cell(r.drop === undefined || r.drop === null ? T("lib.drop.skipped") : r.drop + " V", "mono"));
    const ok = r.pass === true ? "✅ " + T("lib.live.pass") : r.pass === false ? "❌ " + T("lib.live.fail") : "-";
    const td = cell(ok, r.pass === true ? "ok" : r.pass === false ? "bad" : "");
    td.dataset.pass = r.pass === true ? "1" : r.pass === false ? "0" : "";
    tr.appendChild(td);
    tbl.appendChild(tr);
  }
  box.appendChild(tbl);
  return box;
}
