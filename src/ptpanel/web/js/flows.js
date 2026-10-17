"use strict";

/* ================= 工装完整测试 layers 1-2 =================
 *
 * The full test (decisions 125, 128, PORTTOOL-FLOW.md C.3.6 「工装完整测试页」):
 * layer 1 is the title, what will run and 「配置完成，开始测试」, the only
 * control that starts it; layer 2 is the four sub-tests with their ticks,
 * times and run state. Notes, the safety card and the last outcome are
 * full.js; the right column is runview.js.
 */

// Called from pickView when the full test is opened.
function flowLoad() {
  $("#flowwhat").textContent = T("ui.full.what");
  fullSeen = "";
  loadFull();
}

/* ---------------- 工装完整测试 ---------------- */

const fullRunning = () => !!(fullData && fullData.running);
const fullStepInfo = step => ((fullData && fullData.steps) || []).find(s => s.step === step);
const fullAvailable = step => { const s = fullStepInfo(step); return !s || s.available; };
const fullTicked = () => FULL_SUBS.filter(s => fullPick[s.step] && fullAvailable(s.step));

// How many items a sub-test's configuration runs on this board.
function subPicked(k) {
  return catalog.items.filter(it => it.stages.includes(k) && cfgPortOn(k, it.port) && cfgOn(k, it)).length;
}

function subPlanText() {
  const t = fullTicked();
  if (!t.length) { return T("full.plan_none"); }
  return T("full.plan", { list: t.map(s => s.durs ? T("full.plan_item", { name: T(s.name), dur: durLabel(cfgDuration(s.k)) }) : T(s.name)).join(" → ") });
}

// Called from renderSubTest (shell.js) and when /api/full changes.
function fullTop(top, host) {
  top.textContent = ""; host.textContent = "";
  const running = fullRunning();
  top.appendChild(el("h2", null, T("shell.entry.full")));
  const plan = el("span", "sub", subPlanText()); plan.dataset.fullplan = "1";
  top.appendChild(plan);
  top.appendChild(el("span", "sp"));
  const btn = el("button", running ? "stop" : "primary", T(running ? "ui.full.stop" : "full.go"));
  btn.id = "shellstart";
  btn.disabled = !running && !fullTicked().length;
  btn.onclick = running ? stopFullRun : () => startFull("");
  top.appendChild(btn);
  host.appendChild(subTiles(running));
}

function subTiles(running) {
  const w = el("div", "subtiles");
  const st = fullData || {};
  const shown = running || !!st.last;   // run states stay up after a run
  for (const s of FULL_SUBS) {
    const info = fullStepInfo(s.step);
    const avail = !info || info.available;
    const tile = el("div", "subtile" + (s.k === view ? " cur" : ""));
    tile.dataset.sub = s.k;

    const head = el("div", "row");
    const cb = el("input"); cb.type = "checkbox"; cb.dataset.fullstep = s.step;
    cb.checked = !!fullPick[s.step] && avail;
    cb.disabled = !avail || running;
    cb.onclick = e => e.stopPropagation();
    cb.onchange = () => { fullPick[s.step] = cb.checked; renderFullTop(); };
    head.appendChild(cb);
    head.appendChild(el("span", "nm", T(s.name)));
    tile.appendChild(head);

    const time = el("div", "row");
    if (s.durs && s.durs.length === 1) {
      // Nothing to choose: the functional test runs once (decision 139).
      time.appendChild(el("span", "hint", T("full.time")));
      const one = el("span", null, durLabel(s.durs[0])); one.dataset.subonly = s.k;
      time.appendChild(one);
    } else if (s.durs) {
      time.appendChild(el("span", "hint", T("full.time")));
      const sel = el("select"); sel.dataset.subdur = s.k; sel.disabled = running;
      for (const d of s.durs) { const o = el("option", null, durLabel(d)); o.value = d; o.selected = cfgDuration(s.k) === d; sel.appendChild(o); }
      sel.onclick = e => e.stopPropagation();
      sel.onchange = () => cfgSetDuration(s.k, sel.value);   // saved on the spot (stagecfg.js)
      time.appendChild(sel);
    } else {
      time.appendChild(el("span", "hint", T("full.safety_d")));
    }
    tile.appendChild(time);
    if (!avail && info.why) { tile.appendChild(el("div", "sub", info.why)); }

    if (shown) { tile.appendChild(subState(s.step, st)); }
    if (s.durs && !running) {
      const n = el("div", "hint", T("full.picked", { n: subPicked(s.k) })); n.dataset.subn = s.k;
      tile.appendChild(n);
    }
    tile.onclick = () => pickView(s.k); // while running too: its cards are shown locked
    w.appendChild(tile);
  }
  return w;
}

// 等着 / 在跑 / outcome / 没勾跳过, and the step's progress bar (C.3.3 「进度」).
function subState(step, st) {
  const row = el("div", "row");
  const picked = (st.picked || []).includes(step);
  const o = (st.status || {})[step];
  let pill;
  if (!picked) { pill = el("span", "tgtres", T("full.st.skip")); }
  else if (o) { pill = el("span", "tgtres " + (o === "PASS" ? "ok" : "bad"), T(FULL_OUTCOME[o])); pill.dataset.fullres = step; }
  else if (st.running && st.current === step) { pill = el("span", "tgtres", T("ui.full.running")); }
  else { pill = el("span", "tgtres", T("full.st.wait")); }
  pill.dataset.substate = step;
  row.appendChild(pill);
  if (picked) {
    const bar = el("span", "stbar"), fill = el("i");
    fill.style.width = Math.max(0, Math.min(100, (st.pct || {})[step] || 0)) + "%";
    bar.appendChild(fill); row.appendChild(bar);
  }
  return row;
}

// Layers 1 and 2 again, leaving the cards alone.
function renderFullTop() { if (isFullSub(view)) { fullTop($("#rtop"), $("#subtiles")); } }

// 去研发测试: the R&D view on the port to calibrate on (decision 97).
function calButton(port) {
  const b = el("button", null, T("flow.goto_rd"));
  b.dataset.flow = "goto_rd";
  b.onclick = () => { lport = port; pickView("rd"); };
  return b;
}
