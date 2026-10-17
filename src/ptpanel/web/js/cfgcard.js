"use strict";

// The AI / AO / DI / DO card on a configuration page: R&D's shape, settings only, kept in
// /api/stagecfg under the catalogue's item ids (decision 121, PORTTOOL-FLOW.md C.3.6 「配置里的 AI / AO / DI / DO」).

function cfgPortCard(port) {
  const p = port.port, v = view;
  const its = itemsOf(v, p);
  const card = el("div", "item anacard cfgcard"); card.dataset.cfgcard = p;

  // Data source: always the reference board here (decision 129).
  const hd = el("div", "ih");
  hd.appendChild(el("b", null, port.label));
  hd.appendChild(el("span", "sp"));
  const src = el("span", "hint", T("cfg.src_fixed")); src.dataset.cfgsrc = p;
  hd.appendChild(src);
  card.appendChild(hd);
  // Reference board set to unused: say what each item uses instead (PORTTOOL-FLOW.md C.3.6 「标准机「不用」」, STG-27).
  for (const it of its) { const n = cfgSubstNote(v, it); if (n) { card.appendChild(n); } }

  // AI / AO readings and outputs: one row per channel with its test points.
  const reads = its.filter(it => (p === "ain" || p === "aout") && it.channel > 0 && !anaIsCal(it));
  if (reads.length) {
    anaLoad(false);
    const tbl = el("table", "gtab");
    const hr = el("tr");
    for (const h of [T("cfg.col.on"), T("lib.live.col.ch"), T("ana.range"), T("ana.test_points", { unit: "" })]) { hr.appendChild(el("th", null, h)); }
    tbl.appendChild(hr);
    for (const it of reads) {
      const c = anaInfo(it);
      const tr = el("tr"); tr.dataset.item = it.id;
      tr.appendChild(el("td")).appendChild(cfgOnBox(v, it));
      tr.appendChild(el("td", "mono", anaChannel(it)));
      // The range shared by the three fixture tests (decision 139), then the
      // summary that opens to the range block (decision 136). What the reference
      // board as registered cannot test here, in the gate's own words (Server.refCannot).
      const cannot = ((typeof stageCfgs !== "undefined" && stageCfgs && stageCfgs.mismatch) || {})[it.id] || "";
      const rtd = tr.appendChild(el("td"));
      if (c) {
        const ch = anaChannel(it);
        const sel = el("select"); sel.dataset.cfgrange = ch;
        for (const r of c.options || []) { const o = el("option", null, anaRangeName(r)); o.value = r; o.selected = r === c.range; sel.appendChild(o); }
        sel.onchange = () => cfgSetRange(ch, sel.value);
        rtd.appendChild(sel);
        rtd.appendChild(rngSummary(ch, c, Object.assign(anaKbOpts(ch), { cannot })));
      } else {
        rtd.appendChild(el("span", null, "-"));
      }
      const inp = el("input"); inp.dataset.anaPoints = it.id; inp.style.width = "220px";
      const cell = memoCell(inp, "pts|" + it.id, () => anaPointsText(it, v), () => {});
      inp.onchange = () => { if (!inp.classList.contains("badcell")) { cfgSetPoints(v, it, anaParse(inp.value)); } };
      const td = tr.appendChild(el("td"));
      td.appendChild(cell);
      // A test point on a fit point is pointed out, not refused (decision 107).
      td.appendChild(anaOverlapNote(it, v, anaPoints(it, v)));
      tbl.appendChild(tr);
    }
    card.appendChild(tbl);
    card.appendChild(el("div", "hint", T("cfg.range_shared")));
  }

  // The port's other items (DI / DO, AO with a load): one row each, its parameters.
  const rest = its.filter(it => !reads.includes(it));
  if (rest.length) {
    const tbl = el("table", "gtab");
    const hr = el("tr");
    for (const h of [T("cfg.col.on"), T("shell.item.col.item"), T("shell.item.params")]) { hr.appendChild(el("th", null, h)); }
    tbl.appendChild(hr);
    for (const it of rest) {
      const tr = el("tr"); tr.dataset.item = it.id;
      tr.appendChild(el("td")).appendChild(cfgOnBox(v, it));
      tr.appendChild(el("td", null, it.name));
      tr.appendChild(el("td")).appendChild((it.params || []).length ? cfgParamEditor(v, it) : el("span", "hint", "-"));
      tbl.appendChild(tr);
    }
    card.appendChild(tbl);
  }
  // Each item's description, criteria and how it runs (PORTTOOL-FLOW.md C.3.6 layer 3).
  const info = el("div", "cfginfos");
  // AI / AO readings and outputs: one short note, method and criterion only (decision 139).
  if (reads.length) { info.appendChild(cfgAnaNote(p, reads[0])); }
  for (const it of its.filter(x => !anaIsCal(x) && !reads.includes(x))) { info.appendChild(cfgItemInfo(v, it)); }
  card.appendChild(info);
  return card;
}

// How an AI / AO channel is measured and judged; the tolerance is the plan's (anaLimit).
function cfgAnaNote(port, it) {
  const box = el("div", "cfginfo"); box.dataset.cfgananote = port;
  box.appendChild(el("div", null, T(port === "ain" ? "cfg.ana.method_ai" : "cfg.ana.method_ao")));
  const lim = anaLimit(it);
  box.appendChild(el("div", null, lim.check ? T("cfg.ana.crit", { tol: lim.check.max }) : T("shell.item.crit_missing", { step: lim.step || "-" })));
  return box;
}

function cfgOnBox(v, it) {
  const cb = el("input"); cb.type = "checkbox"; cb.dataset.pick = it.id;
  cb.checked = cfgOn(v, it);
  cb.onchange = () => cfgTick(v, it, cb.checked);
  return cb;
}
