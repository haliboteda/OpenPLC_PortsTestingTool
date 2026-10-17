"use strict";

/* ---------------- api ---------------- */
async function api(path, body) {
  const opt = body ? {method:"POST", headers:{"Content-Type":"application/json"},
                      body:JSON.stringify(body)} : {};
  // A request cut off (the page reloading, the panel gone) is an error reply, not a throw.
  let r;
  try { r = await fetch(path, opt); } catch (e) { return {error:T("ui.api.unreachable")}; }
  const j = await r.json().catch(() => ({error:T("ui.api.1")}));
  return localize(j);
}

// Go's sentences arrive as {"$t": key, "args": {...}} and are worded here, in
// the page's language (decision 82).
function localize(v) {
  if (Array.isArray(v)) { return v.map(localize); }
  if (v && typeof v === "object") {
    if ("$t" in v) { return T(v.$t, localize(v.args || {})); }
    const o = {};
    for (const k of Object.keys(v)) { o[k] = localize(v[k]); }
    return o;
  }
  return v;
}

let allPorts = [];

let chosenPort = null;

// A hint, never a choice: the label is all there is, and adapters can share a VID/PID.
function looksLikeRs232(label) {
  return /pl2303|prolific|ft232|ftdi|cp210|ch343|usb-serial ch34/i.test(label);
}

async function loadPorts() {
  const j = await api("/api/ports");
  allPorts = j.ports || [];
  const sel = $("#ctlport"); sel.textContent = "";
  const note = $("#ctlnote"); note.textContent = "";

  if (j.error || !j.ports || !j.ports.length) {
    sel.appendChild(new Option(j.error || T("ui.loadPorts.1"), ""));
    $("#connect").disabled = true;
    return;
  }

  // What was chosen last time. Offered as the selection rather than connected
  // to automatically: opening a serial port is a side effect.
  if (!chosenPort && state && state.saved && state.saved.control &&
      j.ports.some(p => p.name === state.saved.control)) {
    chosenPort = state.saved.control;
  }

  // The simulated board goes last: every reading it gives is invented (decision 128).
  const ports = j.ports.filter(p => p.name !== "sim").concat(j.ports.filter(p => p.name === "sim"));
  sel.appendChild(new Option(T("html.ctl_pick"), ""));
  for (const p of ports) {
    let text = p.label;
    if (looksLikeRs232(p.label)) { text += T("ui.loadPorts.2"); }
    if (p.name === "sim") { text += T(p.unavailable ? "ui.loadPorts.3" : "ui.loadPorts.5"); }
    if (state && state.saved && state.saved.control === p.name) { text += T("ui.loadPorts.6"); }
    const o = new Option(text, p.name);
    // Machines find a port by its name, not by the words on screen.
    o.dataset.port = p.name;
    o.title = p.unavailable || p.label;
    if (p.unavailable) {
      o.disabled = true;
      // Missing is said, not hidden: "there is none" and "not built" look the same otherwise.
      if (p.name === "sim") { note.appendChild(richText("div", "sub", T("ui.loadPorts.4"))); }
    }
    sel.appendChild(o);
  }

  // A port that went away between refreshes must not stay selected.
  if (chosenPort && !ports.some(p => p.name === chosenPort && !p.unavailable)) { chosenPort = null; }
  sel.value = chosenPort || "";
  sel.disabled = !!(state && state.connected);
  $("#connect").disabled = !chosenPort;
}

$("#ctlport").onchange = () => {
  if (state && state.connected) { return; }
  chosenPort = $("#ctlport").value || null;
  $("#connect").disabled = !chosenPort;
  $("#connhint").innerHTML = chosenPort ? T("ui.loadPorts.7", {name: chosenPort}) : T("ui.applyState.2");
};

$("#refresh").onclick = loadPorts;

// Off is a diagnostic: miss climbing while seq stands still proves the counter works.
$("#autoecho").onchange = async () => {
  applyState(await api("/api/autoecho", {on: $("#autoecho").checked}));
};

$("#connect").onclick = async () => {
  if (!chosenPort) { return; }
  $("#connect").disabled = true;
  const j = await api("/api/connect", {port: chosenPort});
  $("#connect").disabled = false;
  if (j.error) { showMessage(j.error, true); return; }
  applyState(j);
  startEvents();
};

$("#disconnect").onclick = async () => {
  if (evtSource) { evtSource.close(); evtSource = null; }
  applyState(await api("/api/disconnect", {}));
};

function startEvents() {
  if (evtSource) evtSource.close();
  evtSource = new EventSource("/api/events");
  evtSource.onmessage = e => {
    const d = JSON.parse(e.data);
    addLog(d.kind, d.msg ? () => localize(d.msg) : d.line, d.at);
    // Faults, resets and disconnects go on top, not only into the log, and refresh the state (decision 77).
    if (d.kind === "panel" && d.alert) {
      refreshState().then(() => showMessage(localize(d.msg), true));
      return;
    }
    if (d.kind === "frame" && d.port) { reads[d.port] = d; }
  };
  evtSource.onerror = () => { /* EventSource retries by itself */ };
}

/* ---------------- state ---------------- */
function applyState(j) {
  const st = j.state || j;
  if (!st.connected && typeof rdForgetUid === "function") { rdForgetUid(); }
  state = st;
  $("#disconnect").hidden = !st.connected;
  $("#connect").hidden = !!st.connected;
  $("#ctlport").disabled = !!st.connected;
  if (st.connected && st.port) { $("#ctlport").value = st.port; }
  $("#menus").classList.toggle("live", !!st.connected);

  $("#toolver").textContent = st.tool ? T("html.toolver", {version: st.tool}) : "";
  // Readings belong to one board: a switch straight to another control port
  // must not leave the last board's frames on screen (C.3.6 「网口」).
  if (st.port !== readsPort) { reads = {}; readsPort = st.port; }
  // One gate for the rest of the page: all of it comes over the control port.
  $("#afterconn").classList.toggle("live", !!st.connected);
  $("#connhint").innerHTML = st.connected
    ? T("ui.applyState.1", {port: st.port})
    : T("ui.applyState.2");

  const s = $("#status");
  if (st.connected && st.linkError) {
    s.classList.add("bad"); s.textContent = T("ui.applyState.3");
  } else if (st.connected) {
    s.classList.remove("bad");
    s.innerHTML = T("ui.applyState.connected", {port: st.port, firmware: st.firmware || "?"});
    // The first two numbers name one set of panel and firmware (decision 120).
    const mm = v => String(v || "").split(".").slice(0, 2).join(".");
    if (st.tool && st.firmware && mm(st.tool) !== mm(st.firmware)) {
      s.appendChild(el("b", "verbad", " " + T("html.ver_mismatch", {tool: st.tool, firmware: st.firmware})));
    }
  } else {
    s.classList.add("bad"); s.textContent = T("ui.applyState.6"); reads = {};
  }
  $("#echobox").hidden = !st.connected;
  renderCriteria(st);
  $("#autoecho").checked = st.autoEcho !== false;
  const es = $("#echostat");
  if (st.echoNote) { es.textContent = T("ui.applyState.7", {echo_note: st.echoNote}); es.style.color = "var(--err)"; }
  else if (st.autoEcho === false) { es.textContent = T("ui.applyState.8"); es.style.color = "var(--warn)"; }
  else { es.textContent = T("ui.applyState.9", {echo_count: (st.echoCount || 0)}); es.style.color = ""; }

  showMessage(st.linkError || st.capsError, true);
  shellOnState(st);
}

// Above the right-hand area, so a fault is seen whatever the menus show.
let readsPort = "";  // the control port the frames in reads came from

function showMessage(text, bad) {
  const host = $("#msgbox");
  const old = host.querySelector(".msg"); if (old) old.remove();
  if (!text) return;
  const m = el("div", "msg" + (bad ? " bad" : "")); m.textContent = text;
  host.prepend(m);
}

async function refreshState() { applyState(await api("/api/state")); }

/* ---------------- criteria plan (header) ---------------- */
// The plan list is fetched once and cached; it only changes when somebody adds
// a file next to the exe.
let critPlans = null;

async function renderCriteria(st) {
  const box = $("#critbox");
  box.hidden = !st.connected;
  if (!st.connected) { return; }

  const sel = $("#critplan");
  if (critPlans === null) {
    critPlans = [];
    try {
      const r = await api("/api/plans");
      critPlans = (r.plans || []).map(p => p.name || p);
    } catch (e) { critPlans = []; }
  }
  const want = st.criteriaPlan || "";
  // The plan in use stays selectable even when the list came back empty.
  const names = critPlans.slice();
  if (want && names.indexOf(want) < 0) { names.unshift(want); }

  const same = sel.options.length === names.length &&
               names.every((n, i) => sel.options[i].value === n);
  if (!same) {
    sel.textContent = "";
    for (const n of names) {
      const o = el("option", null, n);
      o.value = n;
      sel.appendChild(o);
    }
  }
  sel.value = want;

  const ver = $("#critver");
  if (st.criteriaProblem) {
    ver.textContent = T("ui.renderCriteria.1", {criteria_problem: st.criteriaProblem});
    ver.style.color = "var(--err)";
  } else {
    ver.textContent = st.criteriaLimits ? T("ui.renderCriteria.2", {criteria_limits: st.criteriaLimits}) : "";
    // A limit set that is not the default is worth noticing, since a board can
    // pass under it and fail under the strict one.
    ver.style.color = /relax|宽/i.test(st.criteriaPlan || "") ? "var(--warn)" : "";
  }
}

$("#critplan").onchange = async () => {
  const j = await api("/api/criteria", {plan: $("#critplan").value});
  if (j.error) { showMessage(j.error, true); refreshState(); return; }
  // The server has cleared every result judged by the old limits (decision 30).
  addLog("panel", () => T("ui.top.crit_changed", {plan: j.plan, limits: j.limitVersion || "?"}));
  const st = await api("/api/state");
  applyState(st);
  libRefresh();
};
