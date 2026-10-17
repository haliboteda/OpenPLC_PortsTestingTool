"use strict";

// The connection block at the top left (STG-10, decision 103): board time under
// the control port, the far ends of the link ports, the reference board, the
// ST-Link and the reserved instrument row. PORTTOOL-FLOW.md C.3.6, "连接区".

// Each one is the far end of a loop=link port, bound through /api/link.
const CONN_LINKS = [
  { k: "usb",   name: "conn.usb",   d: "conn.usb_d" },
  { k: "eth",   name: "conn.eth",   d: "conn.eth_d" },
  { k: "rs232", name: "conn.rs232", d: "conn.rs232_d", fixed: true },
  { k: "rs485", name: "conn.rs485", d: "conn.rs485_d" },
  { k: "can",   name: "conn.can",   d: "conn.can_d" },
];

let connInfo = { clock: null, ref: {}, stlink: {} };
let connPick = {};       // row -> COM chosen in its dropdown, kept across redraws
let connSig = "";        // what the rows were last drawn from
let connPolling = false;

function connRow(key, label, title) {
  const r = el("div", "crow");
  r.dataset.conn = key;
  r.title = T(title);
  r.appendChild(el("span", "cl", T(label)));
  const v = el("span", "cv");
  r.appendChild(v);
  const dot = el("span", "dot");
  r.appendChild(dot);
  return { r, v, dot };
}

function connSelect(key, choices, preferred) {
  const sel = el("select");
  for (const o of choices) {
    const op = el("option", null, o.label);
    op.value = o.name;
    sel.appendChild(op);
  }
  const want = connPick[key] || preferred;
  if (want && choices.some(o => o.name === want)) { sel.value = want; }
  sel.onchange = () => { connPick[key] = sel.value; };
  return sel;
}

// Serial ports other than the control port; the simulated board never stands
// in for an adapter.
function adapterChoices() {
  return (allPorts || []).filter(p => p.name !== state.port && p.name !== "sim" && !p.unavailable);
}

async function connAct(promise) {
  const j = await promise;
  if (j && j.error) { showMessage(j.error, true); return; }
  if (j && "links" in j) { applyState(j); }
  else if (j && "firmware" in j) { connInfo.ref = j; }   // an /api/ref answer
  refreshConnRows(true);
}

function linkConnRow(c) {
  const { r, v, dot } = connRow(c.k, c.name, c.d);
  if (c.fixed) {
    v.appendChild(el("span", "sub", T("conn.rs232_ctl")));
    return r;
  }
  if (!state.connected) {
    v.appendChild(el("span", "sub", T("conn.need_control")));
    return r;
  }
  const p = (state.ports || []).find(x => x.name === c.k);
  if (!p || p.loop !== "link") {
    v.appendChild(el("span", "sub", T("conn.no_port")));
    return r;
  }
  const bound = linkNow(c.k);
  if (bound) {
    dot.classList.add(bound.error ? "bad" : "ok");
    v.appendChild(el("span", "mono", bound.com));
    const off = el("button", null, T("conn.unbind"));
    off.onclick = () => connAct(api("/api/unlink", { port: c.k }));
    v.appendChild(off);
    const n = el("div", "ccount");
    n.dataset.linkcounts = c.k;
    n.textContent = linkCounts(bound);
    r.appendChild(n);
    if (bound.error) { r.appendChild(el("div", "cerr", T("conn.peer_err", { error: bound.error }))); }
    return r;
  }
  if (c.k === "eth") {
    // Found by the server right after connecting (boardip.go), not from a test.
    const ip = connInfo.boardIP || state.boardIP || {};
    if (!ip.addr) {
      const why = ip.state === "probing" ? T("conn.eth_probing") : (ip.why || T("conn.eth_probing"));
      v.appendChild(el("span", "sub", why));
      v.dataset.ethip = "";
      return r;
    }
    const fr = reads.eth;
    const addr = ip.addr + ":" + ((fr && fr.fields && fr.fields.port) || "5000");
    const shown = el("span", "mono", addr);
    shown.dataset.ethip = ip.addr;
    v.appendChild(shown);
    if (ip.sim) { v.appendChild(el("span", "sub", T("conn.eth_sim"))); }
    const on = el("button", null, T("conn.bind"));
    on.onclick = () => connAct(api("/api/link", { port: "eth", tcp: addr }));
    v.appendChild(on);
    return r;
  }
  const choices = adapterChoices();
  if (!choices.length) {
    v.appendChild(el("span", "sub", T("conn.no_adapter")));
    return r;
  }
  const was = ((state.saved || {}).peers || {})[c.k];
  // The board's own USB port enumerates as an ST virtual COM port.
  const cdc = c.k === "usb" ? choices.find(o => /0483:5740/i.test(o.label || "")) : null;
  const sel = connSelect(c.k, choices, (was && was.com) || (cdc && cdc.name));
  v.appendChild(sel);
  const on = el("button", null, T("conn.bind"));
  on.onclick = () => connAct(api("/api/link", { port: c.k, com: sel.value }));
  v.appendChild(on);
  return r;
}

// A bound peer: bound per the page's state (changed by bind / unbind at once),
// counts and error from the once-a-second /api/conn when it agrees.
function linkNow(k) {
  const b = (state.links || {})[k];
  if (!b) { return null; }
  const p = (connInfo.links || {})[k];
  return p && p.com === b.com ? p : b;
}
function linkCounts(b) { return T("conn.peer_counts", { rx_lines: b.rxLines || 0, echoed: b.echoed || 0 }); }

function refConnRow() {
  const { r, v, dot } = connRow("ref", "conn.ref", "conn.ref_d");
  const ref = connInfo.ref || {};
  if (ref.connected) {
    dot.classList.add("ok");
    v.appendChild(el("span", "mono", ref.port + (ref.firmware ? " · " + ref.firmware : "")));
    const off = el("button", null, T("conn.disconnect"));
    off.onclick = () => connAct(api("/api/ref", { port: "" }));
    v.appendChild(off);
    const set = el("button", null, T("refpg.open")); set.dataset.refopen = "1";
    set.onclick = () => openRefPage();
    v.appendChild(set);
  } else {
    closeRefPage();
    // The simulated board is allowed here even when it is the control port too:
    // each "sim" is its own process.
    const choices = (allPorts || []).filter(p => !p.unavailable && (p.name === "sim" || p.name !== state.port));
    // "Not used" is saved with the stage configs (ref_unused): CAN exchange turns
    // to the PC, the other reference-board items cannot run (decision 135).
    const unused = !!(stageCfgs && stageCfgs.ref_unused);
    const sel = connSelect("ref", choices.concat([{ name: REF_UNUSED, label: T("conn.ref_unused") }]),
                           unused ? REF_UNUSED : null);
    if (unused) { sel.value = REF_UNUSED; }
    sel.onchange = () => {
      connPick.ref = sel.value;
      if ((sel.value === REF_UNUSED) !== unused) { setRefUnused(sel.value === REF_UNUSED); }
    };
    v.appendChild(sel);
    const on = el("button", null, T("conn.connect"));
    on.disabled = unused || !choices.length;
    on.onclick = async () => {
      await connAct(api("/api/ref", { port: sel.value }));
      if ((connInfo.ref || {}).connected && unused) { setRefUnused(false); }
    };
    v.appendChild(on);
  }
  if (ref.error) {
    dot.classList.add("bad");
    r.appendChild(el("div", "cerr", ref.error));
  }
  return r;
}

const REF_UNUSED = "-";


async function setRefUnused(on) {
  const j = await api("/api/stagecfg", { ref_unused: on });
  stageCfgs = j;
  if (j.error) { showMessage(j.error, true); }
  renderShell();
  refreshConnRows(true);
}

function stlinkConnRow() {
  const { r, v, dot } = connRow("stlink", "conn.stlink", "conn.stlink_d");
  const st = connInfo.stlink || {};
  if (!st.checked || st.busy) {
    v.appendChild(el("span", "sub", T("conn.stlink_checking")));
  } else if ((st.probes || []).length) {
    dot.classList.add("ok");
    const p = st.probes[0];
    v.appendChild(el("span", "mono", (p.board || "ST-Link") + " · " + p.sn));
  } else if (st.error) {
    dot.classList.add("bad");
    v.appendChild(el("span", "sub", st.error));
  } else {
    v.appendChild(el("span", "sub", T("conn.stlink_none")));
  }
  const again = el("button", null, T("conn.recheck"));
  again.disabled = !!st.busy;
  again.onclick = async () => {
    again.disabled = true;
    connInfo = await api("/api/conn", { stlink: true });
    refreshConnRows(true);
  };
  v.appendChild(again);
  return r;
}

function reservedConnRow() {
  const { r, v } = connRow("instrument", "conn.instrument", "conn.instrument_d");
  r.classList.add("reserved");
  const sel = el("select");
  sel.disabled = true;
  sel.appendChild(el("option", null, T("conn.reserved")));
  v.appendChild(sel);
  return r;
}

// Redrawn only when what it shows changed, so an open dropdown is not pulled
// away from under the mouse once a second.
let refWasConnected = false;

function refreshConnRows(force) {
  const eth = JSON.stringify(connInfo.boardIP || state.boardIP || {});
  const sig = JSON.stringify([state.connected, state.port, Object.keys(state.links || {}),
    Object.keys(state.links || {}).map(k => (linkNow(k) || {}).error || ""),
    (allPorts || []).map(p => p.name), connInfo.ref, connInfo.stlink, eth, LANG,
    !!(typeof stageCfgs !== "undefined" && stageCfgs && stageCfgs.ref_unused)]);
  if (!force && sig === connSig) { return; }
  connSig = sig;
  // The AI / AO / DI / DO cards' default peer follows the reference board
  // (defaultPeer in shell.js, decision 112).
  const refNow = !!(connInfo.ref || {}).connected;
  if (refNow !== refWasConnected) { refWasConnected = refNow; if (state.connected) { renderShell(); } }
  const host = $("#connrows");
  host.textContent = "";
  for (const c of CONN_LINKS) { host.appendChild(linkConnRow(c)); }
  host.appendChild(knxConnRow());   // knxwin.js
  host.appendChild(refConnRow());
  host.appendChild(stlinkConnRow());
  host.appendChild(reservedConnRow());
}

function renderClock() {
  const box = $("#boardclock");
  const c = connInfo.clock;
  box.classList.remove("bad");
  if (!state.connected) { box.textContent = ""; return; }
  if (!c) { box.textContent = T("conn.clock_wait"); return; }
  if (c.error) { box.classList.add("bad"); box.textContent = T("conn.clock_err", { error: c.error }); return; }
  box.textContent = T("conn.clock", { date: c.date, time: c.time }) + (c.init ? "" : " " + T("conn.clock_unset"));
}

// Once a second: the server reads the board's time at most that often and
// skips a turn while a command is on the control port (conn.go).
async function pollConn() {
  if (connPolling) { return; }
  connPolling = true;
  try {
    connInfo = await api("/api/conn");
    renderClock();
    refreshConnRows(false);
    // Counts change every second: rewritten in place, not by a redraw that
    // would close an open dropdown on another row.
    for (const n of document.querySelectorAll("#connrows [data-linkcounts]")) {
      const b = linkNow(n.dataset.linkcounts);
      if (b) { n.textContent = linkCounts(b); }
    }
  } catch (e) { /* the panel process went away; the next poll says so */ }
  connPolling = false;
}

refreshConnRows(true);
setInterval(pollConn, 1000);
