"use strict";

// The range block: one channel's range, jumpers, formula, full scale and k, b, used on the
// R&D card, the reference board page and configuration cards (decision 136, PORTTOOL-FLOW.md C.3.2 「档位与换算」).
// Formulas come from the server (calstore.SpecsFor); none is restated here.

// The spec of channel info c on range r (default: the registered one).
function rngSpec(c, r) { return ((c && c.specs) || []).find(s => s.id === (r || c.range)) || null; }

// The range's linear formula: AI nominal = raw × gain + offset; AO DAC mV = nominal × gain + offset.
const rngAINominal = (raw, s) => raw * s.gain + s.offset;
const rngAODAC = (v, s) => Math.max(0, Math.round(v * s.gain + s.offset));
const rngAONominal = (mv, s) => (mv - s.offset) / s.gain;

const rngNum = (v, d) => Number(v).toFixed(d);

// Which blocks are expanded, this page session only (decision 137): key is
// "rd|AO1" for the R&D card, "ref|AO1" for the reference-board page.
const rngOpen = {};

// opts: {range: the range to show (default registered), select: the range
// picker or null for read-only, toggle: false to always show the details
// (the configuration summary opens to it), kb: {k, b} on the board or null, calRange:
// the range the local archive was calibrated on, why: why there is no k, b,
// cannot: why the reference board cannot test this channel, the gate's words}.
function rngBlock(ch, c, opts) {
  const s = rngSpec(c, opts.range);
  const box = el("div", "rngblk"); box.dataset.rng = ch;
  const hd = el("div", "row");
  hd.appendChild(el("b", "mono", ch));
  hd.appendChild(el("span", "sub", T("rng.range")));
  if (opts.select) { hd.appendChild(opts.select); } else { hd.appendChild(el("b", null, s ? anaRangeName(s.id) : "-")); }
  if (s && s.default) { hd.appendChild(el("span", "pill", T("rng.default"))); }
  box.appendChild(hd);
  if (opts.cannot) { const n = el("div", "hint warn", opts.cannot); n.dataset.rngcannot = ch; box.appendChild(n); }
  if (!s) { return box; }
  const kb = opts.kb, other = !!(kb && opts.calRange && opts.calRange !== s.id);

  const tbl = el("table", "gtab rngtab");
  const row = (key, node) => {
    const tr = el("tr");
    tr.appendChild(el("th", null, T(key)));
    tr.appendChild(el("td")).appendChild(node);
    tbl.appendChild(tr);
  };
  const jps = el("div");
  for (const j of s.jumpers) { jps.appendChild(el("span", "rngjp " + (j.closed ? "on" : "off"), j.name + " " + j.state)); }
  row("rng.jumpers", jps);
  const f = el("div", "rngf");
  for (const l of s.formula) { f.appendChild(el("div", "mono", l)); }
  f.appendChild(el("div", "sub", s.note));
  row("rng.formula", f);
  row("rng.fs", el("span", null, T("rng.fs_v", { fs: s.fs, unit: s.unit })));

  const k = kb ? rngNum(kb.k, 5) : "1", b = kb ? rngNum(kb.b, 4) : "0";
  const cal = el("div");
  cal.appendChild(el("span", "mono", T("ana.kb", { k, b, unit: s.unit }) + " "));
  cal.appendChild(el("span", "pill " + (kb ? "pass" : "fail"), T(kb ? "rng.kb_board" : "rng.kb_none")));
  if (other) {
    cal.appendChild(el("span", "pill fail", T("rng.kb_other_range", { r: anaRangeName(opts.calRange) })));
  }
  if (!kb && opts.why) { cal.appendChild(el("div", "hint", opts.why)); }
  row("rng.cal", cal);

  const ao = ch.startsWith("AO");
  const fin = el("div", "rngf");
  fin.appendChild(el("div", "mono", ao ? T("rng.final_ao", { k, b }) : T("rng.final_ai", { k, b, unit: s.unit })));
  if (ao) { fin.appendChild(el("div", "mono", T("rng.final_ao_dac"))); }
  if (!kb) { fin.appendChild(el("div", "hint", T(!ao ? "rng.uncal_ai" : s.id === "v" ? "rng.uncal_ao_v" : "rng.uncal_ao_i"))); }
  row("rng.final", fin);
  if (s.warn) { row("rng.warn", el("span", "pill rngwarn", s.warn)); }
  box.appendChild(tbl);
  if (opts.toggle === false) { return box; }

  // Collapsed by default; the problem marks stay in the header so collapsing hides no problem.
  const key = (opts.select ? "rd|" : "ref|") + ch;
  const marks = el("span", "rngmarks");
  if (!kb) { marks.appendChild(el("span", "pill fail", T("rng.kb_none"))); }
  if (other) {
    const m = el("span", "pill fail", T("rng.kb_other_short"));
    m.title = T("rng.kb_other_range", { r: anaRangeName(opts.calRange) });
    marks.appendChild(m);
  }
  const btn = el("button", "rngtgl"); btn.type = "button"; btn.dataset.rngtoggle = ch;
  const show = () => {
    const open = !!rngOpen[key];
    tbl.hidden = !open; marks.hidden = open;
    btn.textContent = T(open ? "rng.less" : "rng.more");
  };
  btn.onclick = () => { rngOpen[key] = !rngOpen[key]; show(); };
  hd.appendChild(btn);
  hd.appendChild(marks);
  show();
  return box;
}

// A configuration's read-only line: range, k, b and where they come from,
// with the same flags as the block; opening it shows the whole block.
function rngSummary(ch, c, opts) {
  const s = rngSpec(c);
  const d = el("details", "rngsum"); d.dataset.rngsum = ch;
  const sm = el("summary");
  sm.appendChild(el("span", null, s ? anaRangeName(s.id) + " (" + s.unit + ")" : "-"));
  const kb = opts.kb;
  if (kb) {
    sm.appendChild(el("span", "mono", "  " + T("ana.kb", { k: rngNum(kb.k, 5), b: rngNum(kb.b, 4), unit: s ? s.unit : "" }) + " "));
    sm.appendChild(el("span", "pill pass", T("rng.kb_board")));
    // With a gate reason the mark is that reason, one sentence (Server.refCannot, STG-40 row 11).
    if (s && opts.calRange && opts.calRange !== s.id && !opts.cannot) {
      sm.appendChild(el("span", "pill fail", T("rng.kb_other_range", { r: anaRangeName(opts.calRange) })));
    }
  } else {
    sm.appendChild(el("span", "pill fail", T("rng.kb_none")));
  }
  // Ranges that do not match the reference board, calibrated on another range, or anything else the gate refuses on.
  if (opts.cannot) { const n = el("span", "pill fail", opts.cannot); n.dataset.rngcannot = ch; sm.appendChild(n); }
  d.appendChild(sm);
  d.appendChild(rngBlock(ch, c, Object.assign({}, opts, { select: null, toggle: false })));
  return d;
}
