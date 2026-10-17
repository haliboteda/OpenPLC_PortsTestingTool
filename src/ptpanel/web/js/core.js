"use strict";

function T(key, args) {
  const e = STRINGS[key];
  let s = e ? (e[LANG] || e.zh) : key;
  if (args) { s = s.replace(/\{([a-z0-9_]+)\}/g, (m, n) => (n in args ? String(args[n]) : m)); }
  return s;
}
// What f() builds, in language l. The exported log is English whatever the
// page shows, so log lines are kept as functions and rebuilt for it.
function inLang(l, f) { const was = LANG; LANG = l; try { return f(); } finally { LANG = was; } }
// Tables hold keys; the text is looked up on every read, so inLang() reaches it.
function tr(o) {
  return new Proxy(o, { get(t, k) {
    const v = t[k];
    if (typeof v === "string") { return T(v); }
    return (v && typeof v === "object") ? tr(v) : v;
  }});
}

const $ = s => document.querySelector(s);
const el = (tag, cls, txt) => { const n = document.createElement(tag);
  if (cls) n.className = cls; if (txt !== undefined) n.textContent = txt; return n; };

document.title = T("html.title");
for (const n of document.querySelectorAll("[data-t]")) { n.innerHTML = T(n.dataset.t); }
for (const n of document.querySelectorAll("[data-t-placeholder]")) { n.placeholder = T(n.dataset.tPlaceholder); }

// The connection column is as wide as it was last dragged (decision 112). A
// layout preference of this viewer only, so the browser keeps it; without
// storage the default width still works.
{
  const KEY = "porttool.leftw", MIN = 200, MAX = 640;
  const shell = $("main.shell"), grip = $("#leftgrip");
  const apply = w => shell.style.setProperty("--leftw", Math.max(MIN, Math.min(MAX, w)) + "px");
  try { const w = Number(localStorage.getItem(KEY)); if (w) { apply(w); } } catch (e) { /* no storage */ }
  grip.title = T("html.leftgrip");
  grip.onpointerdown = ev => {
    ev.preventDefault();
    grip.setPointerCapture(ev.pointerId);
    grip.classList.add("drag");
    const left = shell.getBoundingClientRect().left;
    grip.onpointermove = e => apply(e.clientX - left);
    grip.onpointerup = () => {
      grip.onpointermove = grip.onpointerup = null;
      grip.classList.remove("drag");
      const w = parseInt(getComputedStyle(shell).getPropertyValue("--leftw"), 10);
      try { if (w) { localStorage.setItem(KEY, String(w)); } } catch (e) { /* no storage */ }
    };
  };
}

// Changing language reloads the page, so first say what a reload would lose:
// parameters typed on the R&D cards live only in this page.
{
  const sel = $("#lang");
  sel.title = T("html.lang");
  for (const [code, name] of [["zh", "中文"], ["en", "English"], ["de", "Deutsch"]]) {
    // German is offered before review, marked as such (decision 82, 2026-10-13).
    const o = el("option", null, code === "de" && !DE_REVIEWED ? name + T("html.lang_de_draft") : name); o.value = code; o.selected = code === LANG; sel.appendChild(o);
  }
  sel.onchange = async () => {
    const lose = Object.keys(libEdits).length;
    if (lose && !confirm(T("html.lang_confirm"))) { sel.value = LANG; return; }
    await api("/api/settings", {lang: sel.value});
    location.reload();
  };
}

let state = { connected:false, ports:[], links:{} };
let reads = {};         // port name -> latest frame fields

let evtSource = null;

/* ---------------- log ---------------- */
const MAX_LOG = 20000;
let logBuf = [];        // every line, kept whether shown or not
let paused = false, pausedCount = 0;
// caps is off by default, see isCaps.
let filters = new Set(["ok","err","frame","log","tx","echo","panel"]);

function isEchoTraffic(kind, line) {
  return (kind === "ok" && line.startsWith("OK echo ")) ||
         (kind === "tx" && line.startsWith("> pt.echo "));
}

// The board describing itself (pt.caps): its own log kind, hidden by default but
// kept and exported, because the panel rereads caps after every command.
function isCaps(kind, line) {
  // OK rtc.read is the connection block's once-a-second board time (PORTTOOL-FLOW.md C.3.6);
  // the commands that ask for both fold with their replies.
  return (kind === "ok" && /^OK ((porttool|port|vals|limits|terms|run)=|rtc\.read )/.test(line)) ||
         (kind === "tx" && /^> pt\.(caps|run rtc\.read)$/.test(line));
}

function addLog(kind, line, at) {
  if (typeof line === "string") {
    if (isEchoTraffic(kind, line)) kind = "echo";
    else if (isCaps(kind, line)) kind = "caps";
    // A timed R&D run logs one line per round; its frames and replies have their own filter, off by default (decision 151).
    else if ((kind === "frame" || kind === "ok") && typeof libTimedRun === "function" && libTimedRun()) kind = "timed";
  }
  // pg: the R&D page on screen, so leaving it can keep its own lines (rdvisit.js).
  logBuf.push({kind, line, at: at || nowStamp(), pg: typeof rdVisit !== "undefined" && rdVisit ? rdVisit.tag : ""});
  if (logBuf.length > MAX_LOG) logBuf.splice(0, logBuf.length - MAX_LOG);
  if (paused) { pausedCount++; $("#paused").textContent = T("ui.addLog.1", {paused_count: pausedCount}); return; }
  appendLine(logBuf[logBuf.length-1]);
}
function lineText(e) { return typeof e.line === "function" ? e.line() : e.line; }
function logNode(e) {
  const n = el("span", "l " + e.kind);
  n.appendChild(el("span", "ts", e.at + "  "));
  n.appendChild(document.createTextNode(lineText(e)));
  return n;
}

// One line, appended live. Reading scrollTop/scrollHeight here is what keeps
// the view stuck to the bottom only while the person is already at the bottom.
function appendLine(e) {
  if (!filters.has(e.kind)) return;
  const box = $("#log");
  const stick = box.scrollTop + box.clientHeight >= box.scrollHeight - 30;
  box.appendChild(logNode(e));
  while (box.childElementCount > 4000) box.removeChild(box.firstChild);
  if (stick) box.scrollTop = box.scrollHeight;
}

// The whole pane through one fragment, not appendLine in a loop: each
// appendLine forces a layout, and 4000 of them freeze the page for seconds.
function redrawLog() {
  const box = $("#log");
  const frag = document.createDocumentFragment();
  for (const e of logBuf.slice(-4000)) {
    if (!filters.has(e.kind)) { continue; }
    frag.appendChild(logNode(e));
  }
  box.textContent = "";
  box.appendChild(frag);
  box.scrollTop = box.scrollHeight;
}
function nowStamp() {
  const d = new Date(), p = (n,w=2) => String(n).padStart(w,"0");
  return p(d.getHours())+":"+p(d.getMinutes())+":"+p(d.getSeconds())+"."+p(d.getMilliseconds(),3);
}

$("#pause").onclick = () => {
  paused = !paused;
  $("#pause").textContent = paused ? T("ui.top.1") : T("ui.top.2");
  $("#paused").hidden = !paused;
  if (!paused) { pausedCount = 0; redrawLog(); }
  else { pausedCount = 0; $("#paused").textContent = T("ui.top.3"); }
};
// Clearing leaves a visible separator line and keeps the stream running: new
// lines refill within milliseconds and look the same as the cleared ones.
$("#clear").onclick = () => {
  logBuf = [];
  const at = nowStamp();
  addLog("log", () => T("ui.top.4", {now_stamp: at}));
  redrawLog();
};
// The export is .txt (opens anywhere, attaches anywhere) and holds the whole
// buffer, not the filtered view, in English (LANG-05). Timestamps are
// YYYY-MM-DD HH:MM:SS local time in every language.
function fixedDateTime(d) {
  const p = n => String(n).padStart(2, "0");
  return d.getFullYear() + "-" + p(d.getMonth() + 1) + "-" + p(d.getDate()) + " " +
    p(d.getHours()) + ":" + p(d.getMinutes()) + ":" + p(d.getSeconds());
}
$("#save").onclick = () => {
  const stamp = new Date().toISOString().slice(0,19).replace(/[:T]/g,"");
  const head = [
    "PortTool log  " + fixedDateTime(new Date()),
    "control port: " + (state.port || "(not connected)") +
      "   firmware: " + (state.firmware || "?"),
    "lines: " + logBuf.length,
    "".padEnd(60, "-"),
  ].join("\r\n");
  const text = head + "\r\n" + logBuf.map(e => e.at + "  " + inLang("en", () => lineText(e))).join("\r\n") + "\r\n";
  const a = el("a");
  a.href = URL.createObjectURL(new Blob([text], {type:"text/plain;charset=utf-8"}));
  a.download = "porttool-" + stamp + ".txt";
  a.click(); URL.revokeObjectURL(a.href);
};
document.querySelectorAll(".filt").forEach(c => c.onchange = () => {
  filters = new Set([...document.querySelectorAll(".filt:checked")].map(x=>x.value));
  filters.add("tx");
  redrawLog();
});

/* ---------------- send ---------------- */
// The command itself reaches the log from the server, like every command the panel sends.
async function run(cmd) {
  const j = await api("/api/command", {cmd});
  if (j.error) showMessage(j.error, true);
  else if (j.refused) showMessage(T("ui.run.1", {refused: j.refused}), true);
  else showMessage("", false);
  if (j.state) applyState(j);
  // Returned so a caller can judge the board's own reply.
  return j;
}

$("#send").onclick = () => {
  const v = $("#raw").value.trim();
  if (!v) return;
  $("#raw").value = "";
  // A typed pt.caps turns the hidden caps kind on: the person asked to see it.
  if (/^pt\.caps/.test(v) && !filters.has("caps")) {
    filters.add("caps");
    const box = document.querySelector('.filt[value="caps"]');
    if (box) { box.checked = true; }
    addLog("log", () => T("ui.top.13"));
    redrawLog();
  }
  run(v);
};
$("#raw").addEventListener("keydown", e => { if (e.key === "Enter") $("#send").click(); });
