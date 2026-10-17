"use strict";

// The CAN analyser window (decision 111, PORTTOOL-FLOW.md C.3.6 "CAN 分析"):
// the can.bus item's live state drawn in a window of its own, outside the
// cards, because the cards are redrawn every poll and would wipe what is being
// typed (same reason as #libask). Closing the window does not stop the item.

const CANWIN_LEC = ["canwin.lec.0", "canwin.lec.1", "canwin.lec.2", "canwin.lec.3",
  "canwin.lec.4", "canwin.lec.5", "canwin.lec.6", "canwin.lec.7"];
const CANWIN_STATE = { active: "canwin.state.active", warning: "canwin.state.warning",
  passive: "canwin.state.passive", off: "canwin.state.off" };
const CANWIN_MATCH = { wait: "canwin.match.wait", ok: "canwin.match.ok", bad: "canwin.match.bad",
  missing: "canwin.match.missing", unsent: "canwin.match.unsent" };
const CANWIN_BAUDS = [125000, 250000, 500000, 1000000];

let canWin = { id: null, st: null, shown: false, paused: false, filter: "", wasRunning: false };

function canWinAct(body) { return liveAct(canWin.id, body); }

function canWinBuild() {
  return anaWinBuild({ win: canWin, id: "canwin", p: "cw", title: "canwin.title", hint: "canwin.hint",
    filter: "canwin.filter", stop: "can_stop", clear: "can_clear", form: canWinForm, draw: canWinDraw,
    head: (head, act) => {
      const baud = el("select"); baud.id = "cwbaud";
      for (const b of CANWIN_BAUDS) { baud.appendChild(new Option(b / 1000 + " kbit/s", String(b))); }
      const setBaud = el("button", null, T("canwin.baud_set"));
      setBaud.id = "cwbaudset";
      setBaud.onclick = () => act({ op: "can_baud", baud: Number(baud.value) });
      head.appendChild(el("span", "hint", T("canwin.baud")));
      head.appendChild(baud); head.appendChild(setBaud);
    } });
}

function canWinForm(side) {
  const f = el("div", "cwform");
  f.dataset.cwside = side;
  f.appendChild(el("b", null, T(side === "board" ? "canwin.send.board" : "canwin.send.pc")));
  const field = (name, label, size) => anaWinField(f, "cw", name, label, size);
  const id = field("id", "canwin.f.id", 5), data = field("data", "canwin.f.data", 24);
  const every = field("every", "canwin.f.every", 6), count = field("count", "canwin.f.count", 5);
  const go = el("button", "primary", T("canwin.send"));
  go.dataset.cwsend = side;
  go.onclick = () => canWinAct({ op: "can_send", side, can_id: id.value, data: data.value,
    every: Number(every.value) || 0, count: Number(count.value) || 1 });
  // No stop of its own: 停止发包 at the top stops both sides (STG-40 row 12).
  f.appendChild(go);
  const rep = el("div", "hint"); rep.dataset.cwrepeat = side;
  f.appendChild(rep);
  return f;
}

// Called by libLive for the can.bus card on every redraw.
function canWinCard(it, st) { return anaWinCard(canWin, "can", "canwin.summary", canWinDraw, it, st); }

function canWinDraw(force) {
  const w = $("#canwin") || canWinBuild();
  const st = canWin.st;
  w.hidden = !canWin.shown || !st;
  if (w.hidden || !st) { return; }
  const v = st.live.can || {}, run = !!st.running;
  w.dataset.running = run ? "1" : "0";
  for (const n of w.querySelectorAll("button, input, select")) {
    if (n.id !== "cwclose" && n.id !== "cwpause" && n.id !== "cwfilter") { n.disabled = !run; }
  }
  const baud = $("#cwbaud");
  if (document.activeElement !== baud && v.baud) { baud.value = String(v.baud); }

  const b = v.board || {}, p = v.pc || {};
  const status = $("#cwstatus");
  status.textContent = "";
  status.appendChild(el("div", b.state === "off" || b.state === "passive" ? "bad" : null, T("canwin.board_status", {
    tx: b.tx || 0, rx: b.rx || 0, lost: b.lost || 0, tec: b.tec || 0, rec: b.rec || 0,
    state: T(CANWIN_STATE[b.state] || "canwin.state.unknown"), lec: T(CANWIN_LEC[b.lec || 0] || "canwin.lec.0") })));
  status.appendChild(el("div", null, T("canwin.pc_status", { tx: p.tx || 0, rx: p.rx || 0, bells: p.bells || 0 })));
  for (const [side, s] of [["board", b], ["pc", p]]) {
    const r = w.querySelector('[data-cwrepeat="' + side + '"]');
    r.textContent = s.every ? T("canwin.repeating", { every: s.every, left: s.left }) : "";
  }
  $("#cwsum").textContent = T("canwin.summary", { ok: v.matched || 0, miss: v.missing || 0, bad: v.bad || 0 });
  if (canWin.paused && !force) { return; }

  const host = $("#cwtable");
  host.textContent = "";
  const tbl = el("table", "quad live cw");
  const tr = el("tr");
  for (const h of ["canwin.col.time", "canwin.col.who", "canwin.col.dir", "canwin.col.id",
    "canwin.col.dlc", "canwin.col.data", "canwin.col.match"]) { tr.appendChild(el("th", null, T(h))); }
  tbl.appendChild(tr);
  const want = canWin.filter.toLowerCase().replace(/^0x/, "");
  const rows = (v.rows || []).filter(r => !want || r.id.toLowerCase().replace(/^0x0*/, "").includes(want.replace(/^0*/, "")));
  for (const r of rows.slice().reverse()) {
    const row = el("tr");
    row.dataset.cwrow = r.side + "-" + r.dir;
    row.dataset.match = r.match || "";
    row.appendChild(el("td", "mono", (r.ms / 1000).toFixed(3)));
    row.appendChild(el("td", null, T(r.side === "board" ? "canwin.who.board" : "canwin.who.pc")));
    row.appendChild(el("td", null, T(r.dir === "tx" ? "canwin.dir.tx" : "canwin.dir.rx")));
    row.appendChild(el("td", "mono", r.id));
    row.appendChild(el("td", "mono", String(r.dlc)));
    row.appendChild(el("td", "mono", r.data || "-"));
    const m = r.match ? T(CANWIN_MATCH[r.match] || "canwin.match.wait", { got: r.got || "-" }) : "";
    row.appendChild(el("td", r.match === "ok" ? "ok" : (r.match && r.match !== "wait") ? "bad" : null, m));
    tbl.appendChild(row);
  }
  if (!rows.length) { host.appendChild(el("div", "hint", T("canwin.none"))); }
  host.appendChild(tbl);
  if (v.dropped) { host.appendChild(el("div", "hint", T("canwin.dropped", { n: v.dropped }))); }
}
