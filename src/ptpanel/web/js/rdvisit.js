"use strict";

// R&D port pages (decision 137, PORTTOOL-FLOW.md C.3.6 「研发四张卡的会话」
// 「离开端口页」). Inputs read only between their start and stop buttons, outputs
// go on by button; the AI / AO card has one per block (decision 151). Leaving a page
// ends its session (the ticked rows are judged) and keeps the round's
// results, log and time in the page memo; coming back shows them.

// Each card's session item and the items its other tables record on.
const RD_SESSION = {
  ain:  { id: "ain.live",    input: true },
  din:  { id: "din.quad",    input: true, also: ["din.level"] },
  aout: { id: "aout.manual", input: false },
  dout: { id: "dout.manual", input: false },
};
const RD_SESSION_IDS = new Set(Object.values(RD_SESSION).map(s => s.id));
// The DO table each 输出 records on.
const RD_DO_ITEM = { drop: "dout.load", blink: "dout.blink", pwm: "dout.square" };

let rdTicks = {};       // "<table>|<ch>" -> ticked (memo chk|<table>|<ch>)
let rdSnap = {};        // "<UID>|<port>" -> {at, res, log}: the last round per board, kept on leaving
let rdUid = "";         // the connected board's UID (/api/uid); "" until known
let rdUidAsking = false;
let rdVisit = null;     // {port, tag, at, uid}: the R&D page on screen, when it last tested, on which board
let rdVisits = 0;
let rdBusy = false;     // a session being ended on leaving, or an output being set up
let rdStarting = null;  // an input session being opened (rdRead)
let rdReadWhy = "";     // why the last 开始读取 failed, said on the card
let rdDoBlock = "";     // the DO table whose 输出 is on
let rdLeaving = Promise.resolve();   // the last page's session ending; starts wait for it

/* ---------------- ticks ---------------- */

// Rows start unticked: nothing is judged that nobody chose.
function rdTicked(table, ch) {
  if (table === "pwm") { return !!dioPwmRow(ch).on; }
  return !!rdTicks[table + "|" + ch];
}

function rdTickList(table, n) {
  const out = [];
  for (let ch = 1; ch <= n; ch++) { if (rdTicked(table, ch)) { out.push(ch); } }
  return out;
}

function rdTickBox(table, ch) {
  const cb = el("input"); cb.type = "checkbox"; cb.dataset.rdtick = table + "|" + ch;
  cb.checked = rdTicked(table, ch);
  cb.onchange = () => {
    if (table === "pwm") { dioPwmRow(ch).on = cb.checked; } else { rdTicks[table + "|" + ch] = cb.checked; }
    dioRetick(table);
    renderRight();
  };
  return cb;
}

/* ---------------- the card's session ---------------- */

const rdState = port => libState(RD_SESSION[port].id) || null;
const rdLive = port => { const s = rdState(port); return s && s.running && s.live ? s.live : null; };

// True while the only thing running is an R&D card's session: the card's own
// settings stay editable then.
function rdOnlyCard() {
  if (!libRunning()) { return false; }
  return Object.entries(libData.states || {}).every(([id, s]) => !s.running || RD_SESSION_IDS.has(id));
}

// Opens the card's session; also are the items its tables record on, sent
// unticked beside it (live.go liveTicked). Resolves once frames come.
async function rdStart(port, also) {
  const s = RD_SESSION[port];
  const items = [{ id: s.id, on: true, params: {}, peer: "human" }]
    .concat((also || []).map(id => ({ id, on: false })));
  const j = await api("/api/lib/start", { stage: "rd", duration: "once", items });
  if (j.error) { return j.error; }
  for (let i = 0; i < 25; i++) {
    await libRefresh();
    if (rdLive(port)) { break; }
    await new Promise(r => setTimeout(r, 200));
  }
  if (!rdLive(port)) { return T("ana.no_frames"); }
  if (!s.input) { rdTouch(); }
  for (const r of rdRefsOf(port)) { await rdRef(port, r.block, r.ch, r.value, true); }
  return "";
}

// What the person typed on the card, sent once a session opens.
function rdRefsOf(port) {
  if (port === "ain" || port === "aout") { return anaRefs(port); }
  return dioRefs(port);
}

// One typed value to the running session; quiet when none runs.
async function rdRef(port, block, ch, value, quiet) {
  if (!rdLive(port)) { return; }
  const j = await api("/api/lib/live", { id: RD_SESSION[port].id, op: "ref", block, ch, value: String(value ?? "") });
  if (j.error && !quiet) { showMessage(j.error, true); }
  if (!quiet) { libRefresh(); }
}

async function rdSet(port, args) {
  if (!rdLive(port)) { return true; }
  const j = await api("/api/lib/live", { id: RD_SESSION[port].id, op: "set", args });
  if (j.error) { showMessage(j.error, true); return false; }
  return true;
}

// The rows to judge when the session ends, per table.
function rdTickedOf(port) {
  if (port === "ain" || port === "aout") { return anaTickedOf(port); }
  if (port === "din") { return { level: dioLevelJudged(), "": rdTickList("quad", 4) }; }
  return rdDoBlock ? { [rdDoBlock]: rdTickList(rdDoBlock, 8) } : {};
}

// Ends the card's session: the ticked rows are judged and recorded. True
// when some row was ticked, which makes it a test.
async function rdEnd(port) {
  const st = rdState(port);
  if (!(st && st.running)) { return false; }
  const ticked = rdTickedOf(port);
  const tested = Object.values(ticked).some(l => l.length);
  if (tested) { rdTouch(); }
  await api("/api/lib/live", { id: RD_SESSION[port].id, op: "end", ticked });
  if (port === "dout") { rdDoBlock = ""; }
  for (let i = 0; i < 25 && (rdState(port) || {}).running; i++) {
    await libRefresh();
    if ((rdState(port) || {}).running) { await new Promise(r => setTimeout(r, 200)); }
  }
  return tested;
}

// 开始读取 / 停止读取 on top of an input card: nothing reads until pressed.
function rdReadBtn(port) {
  const on = !!rdLive(port);
  const b = el("button", on ? "stop" : "primary", T(on ? "rd.read_stop" : "rd.read"));
  b.dataset.rdread = port;
  b.disabled = rdBusy || !!rdStarting || libPageBusy || !!anaCardBusy || !state.connected || (libRunning() && !on);
  b.onclick = () => rdRead(port);
  return b;
}

// Stopping judges the ticked rows (rdEnd), as leaving does.
async function rdRead(port) {
  if (rdLive(port)) {
    rdBusy = true; renderRight();
    try { await rdEnd(port); } finally { rdBusy = false; renderRight(); }
    return;
  }
  if (memoBlocked()) { return; }
  rdReadWhy = "";
  rdStarting = (async () => {
    await rdLeaving;
    const why = await rdStart(port, RD_SESSION[port].also);
    rdReadWhy = why || "";
    if (!why && port === "din") { dioPeriodSet(); }
    // The page was left while the session opened: nobody looks at it.
    if (!why && (view !== "rd" || lport !== port)) { await rdEnd(port); }
  })().finally(() => { rdStarting = null; renderRight(); });
  renderRight();
}

// Said on an input card while its reading is not running.
function rdReadNote(port) {
  if (rdLive(port) || rdStarting) { return el("span"); }
  const n = el("div", "hint", rdReadWhy ? T("rd.read_failed", { why: rdReadWhy }) : T("rd.read_idle"));
  n.dataset.rdnote = port;
  return n;
}

/* ---------------- leaving and coming back ---------------- */

// Called by renderRight for an R&D port page.
function rdEnter(port) {
  if (rdVisit && rdVisit.port === port) { if (!rdVisit.uid) { rdAskUid(); } return; }
  rdVisit = { port, tag: "v" + (++rdVisits), at: null, uid: rdUid };
  if (!rdUid) { rdAskUid(); }
}

// Snapshots are kept per board (STG-39): without a UID nothing is kept or shown.
async function rdAskUid() {
  if (rdUidAsking || !state.connected) { return; }
  rdUidAsking = true;
  try {
    const j = await api("/api/uid");
    if (j && j.uid && state.connected) {
      rdUid = j.uid;
      if (rdVisit && !rdVisit.uid) { rdVisit.uid = rdUid; renderRight(); }
    }
  } catch (e) { /* asked again on the next page */ }
  rdUidAsking = false;
}

// Disconnected: the round so far stays with its board, and the next board's
// UID is asked afresh.
function rdForgetUid() {
  if (!rdUid && !(rdVisit && rdVisit.uid)) { return; }
  rdUid = "";
  if (rdVisit) { rdKeep(rdVisit); rdVisit = { port: rdVisit.port, tag: "v" + (++rdVisits), at: null, uid: "" }; }
}

const rdSnapKey = (uid, port) => uid + "|" + port;

// Something on this page was measured: its time becomes the last test time.
function rdTouch() { if (rdVisit) { rdVisit.at = new Date(); } }

// Polled with /api/lib: a running item of this page is a test going on; an
// input card's reading alone is not, its ticked rows are (rdEnd).
function rdOnRefresh() {
  if (!rdVisit || view !== "rd") { return; }
  const ids = new Set(itemsOf("rd", rdVisit.port).map(it => it.id).filter(id => id !== "ain.live" && id !== "din.quad"));
  if (Object.entries(libData.states || {}).some(([id, s]) => s.running && ids.has(id))) { rdTouch(); }
}

// The round as it stands: each item's ending and its channels, and the log
// lines written while the page was on screen (no sample frames).
function rdRound(v) {
  const res = [];
  for (const it of itemsOf("rd", v.port)) {
    const st = libState(it.id);
    if (!st || st.running || !st.outcome || st.outcome === "SKIPPED") { continue; }
    res.push({ id: it.id, o: st.outcome, ch: (st.chans || []).map(c => [c.name, c.outcome]) });
  }
  const log = logBuf.filter(e => e.pg === v.tag && e.kind !== "frame").slice(-200)
    .map(e => e.at + "  " + lineText(e));
  return { at: v.at.toISOString(), res, log };
}

function rdKeep(v) {
  if (!v || !v.at || !v.uid) { return; }
  rdSnap[rdSnapKey(v.uid, v.port)] = rdRound(v);
  memoSoon();
}

// Leaving the page on screen: its session ends (results recorded), outputs
// go off, then the round is kept. Sends nothing when no R&D page was shown.
function rdLeave() {
  const v = rdVisit;
  rdVisit = null;
  if (!v) { return; }
  rdBusy = true;
  rdLeaving = (async () => {
    try {
      if (rdStarting) { await rdStarting; }
      if (v.port === "aout") { await anaAoOff(1); await anaAoOff(2); }
      if (RD_SESSION[v.port] && await rdEnd(v.port)) { v.at = new Date(); }
      await libRefresh();
      rdKeep(v);
    } finally {
      rdBusy = false; rdReadWhy = "";
      renderRight();
    }
  })();
}

// Best effort when the page closes: the requests may not finish.
function rdUnload() {
  const v = rdVisit;
  if (!v) { return; }
  const post = (url, body) => fetch(url, { method: "POST", keepalive: true,
    headers: { "Content-Type": "application/json" }, body: JSON.stringify(body) }).catch(() => {});
  if (v.port === "aout" && (anaAoOn[1] || anaAoOn[2])) { post("/api/command", { cmd: "pt.stop aout" }); }
  const st = RD_SESSION[v.port] && rdState(v.port);
  if (st && st.running) {
    const ticked = rdTickedOf(v.port);
    post("/api/lib/live", { id: RD_SESSION[v.port].id, op: "end", ticked });
    if (Object.values(ticked).some(l => l.length)) { v.at = new Date(); }
  }
  if (v.at && v.uid) {
    rdSnap[rdSnapKey(v.uid, v.port)] = rdRound(v);
    post("/api/memo", { section: "rd", cells: memoCollect().rd });
  }
}

function rdTimeText(iso) {
  const d = new Date(iso);
  if (isNaN(d)) { return iso; }
  const p = n => String(n).padStart(2, "0");
  return d.getFullYear() + "-" + p(d.getMonth() + 1) + "-" + p(d.getDate()) + " " + p(d.getHours()) + ":" + p(d.getMinutes()) + ":" + p(d.getSeconds());
}

// The right half of an R&D port page's header: the run's stats, the last test
// time, what that round left and its log; only "not tested yet" for a port
// with no round kept (decision 139, PORTTOOL-FLOW.md C.3.6 「页头和日志」).
function rdLastBox(port) {
  const s = rdVisit && rdVisit.uid ? rdSnap[rdSnapKey(rdVisit.uid, port)] : null;
  const box = el("div", "lasttest"); box.dataset.lasttest = port;
  if (!s && !libRunning()) {
    box.appendChild(el("div", "never", T("rd.never_tested"))).dataset.never = port;
    return box;
  }
  box.appendChild(libRunNote());
  if (!s) { return box; }
  const hd = el("div");
  hd.appendChild(el("b", null, T("rd.last_time") + " "));
  const t = el("span", "mono", rdTimeText(s.at));
  t.dataset.lasttime = port;
  hd.appendChild(t);
  box.appendChild(hd);
  if ((s.res || []).length) {
    const row = el("div", "chpills");
    for (const r of s.res) {
      const it = (catalog.items || []).find(x => x.id === r.id);
      const name = it ? it.name : r.id;
      if (r.ch && r.ch.length) {
        for (const [cn, o] of r.ch) { row.appendChild(el("span", "pill " + (LIB_PILL[o] || ""), name + " · " + cn + " " + T(LIB_OUTCOME[o] || "lib.o.error"))); }
      } else {
        row.appendChild(el("span", "pill " + (LIB_PILL[r.o] || ""), name + " " + T(LIB_OUTCOME[r.o] || "lib.o.error")));
      }
    }
    row.dataset.lastres = port;
    box.appendChild(row);
  } else {
    box.appendChild(el("div", "hint", T("rd.no_results")));
  }
  if ((s.log || []).length) {
    const d = el("details");
    d.appendChild(el("summary", null, T("rd.last_log", { n: s.log.length })));
    const pre = el("pre", "mono events", s.log.join("\n"));
    pre.dataset.lastlog = port;
    d.appendChild(pre);
    box.appendChild(d);
  }
  return box;
}
