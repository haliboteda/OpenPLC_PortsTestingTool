"use strict";

// AI / AO as two cards each, one row per channel (decision 114,
// PORTTOOL-FLOW.md C.3.2 / C.3.6 「AI / AO 卡片」). The members stay separate
// catalogue items - each runs and is judged on its own, configurations keep
// their ids - the card only draws them together.

// A port's cards: single items, and groups gathered under their first member.
function cardsOf(v, port) {
  const out = [], seen = {};
  for (const it of itemsOf(v, port)) {
    if (!it.group) { out.push({ items: [it] }); continue; }
    if (!seen[it.group]) { seen[it.group] = { group: it.group, items: [] }; out.push(seen[it.group]); }
    seen[it.group].items.push(it);
  }
  return out;
}

// What a group's tick switches: the channels, and in aging the load hold.
const groupRuns = (v, c) => c.items.filter(it => !it.live && (!it.load || v !== "rd"));
const cardOn = (v, c) => (c.group ? groupRuns(v, c) : c.items).some(it => itemOnIn(v, it));

// In R&D a group's live card and load row start unticked: they wait for a
// person and are started from their own row.
function groupDefaultOff(v, it) { return !!it.group && v === "rd" && (it.live || !!it.load); }

let anaPeek = null;      // /api/analog/peek: the AI channels before anything runs
let anaPeekTimer = null;

// solo: an R&D 「只测一次」 card, started from its own button (decision 116).
function groupCard(c, solo) {
  const first = c.items[0];
  const on = solo || cardOn(view, c);
  const card = el("div", "item grp" + (on ? "" : " off"));
  card.dataset.group = c.group;

  const hd = el("div", "ih");
  const cb = el("input"); cb.type = "checkbox"; cb.checked = on; cb.dataset.pickgroup = c.group;
  cb.disabled = libRunning();
  cb.onchange = () => {
    for (const it of groupRuns(view, c)) {
      if (isCfgView(view)) { cfgTick(view, it, cb.checked); } else { itemOff[view + "|" + it.id] = !cb.checked; }
    }
    renderRight();
  };
  if (!solo) { hd.appendChild(cb); }
  hd.appendChild(el("b", null, first.group_name));
  // Runs the channels ticked in the table.
  if (solo) { hd.appendChild(libItemStart(groupRuns(view, c).filter(it => itemOnIn(view, it)).map(it => it.id))); }
  hd.appendChild(el("span", "sp"));
  hd.appendChild(groupPill(c));
  card.appendChild(hd);

  const body = el("div", "gb");
  body.appendChild(el("div", null, first.group_what));
  const chans = c.items.filter(it => it.channel > 0);
  if (chans.length && ITEM_BODIES[chans[0].id]) { body.appendChild(el("div", "hint", ITEM_BODIES[chans[0].id].crit(chans[0], view))); }

  const tbl = el("table", "gtab");
  const hr = el("tr");
  for (const k of ["ana.col.ch", "ana.col.setup", "ana.col.peer", "ana.col.want_got"]) { hr.appendChild(el("th", null, T(k))); }
  tbl.appendChild(hr);
  for (const it of chans) { tbl.appendChild(groupRow(it, c)); }
  for (const it of c.items.filter(x => x.load)) { tbl.appendChild(groupRow(it, c)); }
  body.appendChild(tbl);

  // The live card of the group (AI live reading, AO manual output) under the table.
  for (const it of c.items.filter(x => x.live)) {
    const sub = el("div", "glive");
    sub.dataset.item = it.id;
    const lh = el("div", "ih");
    if (!solo) { lh.appendChild(rowPick(it)); }
    lh.appendChild(el("b", null, it.name));
    if (solo) { lh.appendChild(libItemStart([it.id])); }
    lh.appendChild(el("span", "sp"));
    lh.appendChild(libPill(it));
    sub.appendChild(lh);
    const st = libState(it.id);
    if (st) { sub.appendChild(libResult(it)); }
    body.appendChild(sub);
  }
  card.appendChild(body);
  if (c.group === "ain.read") { anaPeekStart(); }
  return card;
}

// One member's own tick, so a single channel can be run.
function rowPick(it) {
  const key = view + "|" + it.id;
  const p = el("input"); p.type = "checkbox"; p.dataset.pick = it.id;
  p.checked = itemOnIn(view, it); p.disabled = libRunning();
  p.onchange = () => {
    if (isCfgView(view)) { cfgTick(view, it, p.checked); return; }
    itemOff[key] = !p.checked;
    renderRight();
  };
  return p;
}

function groupRow(it, c) {
  const tr = el("tr");
  tr.dataset.item = it.id;
  const ch = el("td", "ih");
  ch.appendChild(rowPick(it));
  ch.appendChild(el("b", null, it.channel ? anaChannel(it) : T("ana.load_row")));
  ch.appendChild(libPill(it));
  tr.appendChild(ch);

  const setup = el("td");
  if ((it.params || []).length) { setup.appendChild(isCfgView(view) ? cfgParamEditor(view, it) : libParamEditor(it)); }
  if (ITEM_BODIES[it.id]) { setup.appendChild(ITEM_BODIES[it.id].params(it, view)); }
  tr.appendChild(setup);

  tr.appendChild(el("td", null)).appendChild(peerPicker(it));

  const res = el("td");
  if (c.group === "ain.read" && it.channel) {
    const now = el("div", "hint"); now.dataset.peek = it.channel;
    now.textContent = anaPeekText(it.channel);
    res.appendChild(now);
  }
  if (it.load && view !== "rd") { res.appendChild(el("div", "hint", T("ana.load_with_repeat"))); }
  res.appendChild(ITEM_BODIES[it.id] ? ITEM_BODIES[it.id].result(it, view) : libResult(it));
  tr.appendChild(res);
  return tr;
}

function peerPicker(it) {
  const key = view + "|" + it.id;
  const peers = peersIn(view, it);
  if (peers.length < 2) { return el("div", null, T(PEER_NAME[peers[0]])); }
  const ps = el("select"); ps.dataset.peer = it.id;
  for (const p of peers) {
    const o = el("option", null, T(PEER_NAME[p])); o.value = p;
    o.selected = (isCfgView(view) ? cfgPeer(view, it) : (itemPeer[key] || defaultPeer(it))) === p; ps.appendChild(o);
  }
  ps.onchange = () => { if (isCfgView(view)) { cfgSetPeer(view, it, ps.value); } else { itemPeer[key] = ps.value; } };
  return ps;
}

// The worst of the members' states, so the header says whether the card passed.
function groupPill(c) {
  const ms = c.items.filter(it => !it.live);
  const rank = { FAIL: 6, ERROR: 5, TIMEOUT: 5, STOPPED: 3, PASS: 2, RECORDED: 1, SKIPPED: 0 };
  let pick = null, best = -1;
  for (const it of ms) {
    const st = libState(it.id);
    if (st && st.running) { return libPill(it); }
    const r = st && st.outcome in rank ? rank[st.outcome] : -1;
    if (r > best) { best = r; pick = it; }
  }
  return libPill(pick || ms[0] || c.items[0]);
}

/* ---------------- AI: the present reading before a run ---------------- */

function anaPeekText(ch) {
  const a = anaPeek && (anaPeek.analog || []).find(x => x.ch === ch);
  if (!a || a.value == null) { return T("ana.now_none"); }
  return T("ana.now", { value: anaFmt(a.value, a.unit) + " " + (a.unit || "") });
}

// Polled while the AI reading card is on screen and nothing runs.
function anaPeekStart() {
  if (anaPeekTimer) { return; }
  const tick = async () => {
    const card = document.querySelector('#itemcards [data-group="ain.read"]');
    if (!card || view !== "rd" || !state.connected) { clearInterval(anaPeekTimer); anaPeekTimer = null; anaPeek = null; return; }
    if (libRunning() || libPageBusy) { return; }
    const j = await api("/api/analog/peek");
    if (!j || j.busy || j.error) { return; }
    anaPeek = j;
    for (const n of document.querySelectorAll("#itemcards [data-peek]")) { n.textContent = anaPeekText(Number(n.dataset.peek)); }
  };
  anaPeekTimer = setInterval(tick, 1500);
  tick();
}
