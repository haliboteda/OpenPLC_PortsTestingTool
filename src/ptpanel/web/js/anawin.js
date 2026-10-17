"use strict";

// The shell the CAN and KNX analyser windows share (canwin.js, knxwin.js):
// head with stop-all / end / close, status, the two send forms, the
// pause / clear / filter bar and the table host. Built once, outside the
// cards, so typing survives a poll.
//
// cfg: win (the window's state object), id (element id), p (id prefix),
// title, hint, filter (string keys), stop / clear (ops), head (optional,
// adds controls after the title), form (side -> element), draw.
function anaWinBuild(cfg) {
  const win = cfg.win, act = body => liveAct(win.id, body);
  const w = el("div"); w.id = cfg.id; w.hidden = true;
  const head = el("div", "cwhead");
  head.appendChild(el("b", null, T(cfg.title)));
  if (cfg.head) { cfg.head(head, act); }
  // Stops both sides' repeats; the session stays up (decision 151 #7).
  const stopAll = el("button", null, T("canwin.stop_all"));
  stopAll.id = cfg.p + "stopall";
  stopAll.onclick = async () => { await act({ op: cfg.stop, side: "board" }); await act({ op: cfg.stop, side: "pc" }); };
  head.appendChild(stopAll);
  const end = el("button", "stop", T("lib.live.end"));
  end.id = cfg.p + "end";
  end.onclick = () => act({ op: "end" });
  const close = el("button", null, T("canwin.close"));
  close.id = cfg.p + "close";
  close.onclick = () => { win.shown = false; w.hidden = true; };
  head.appendChild(end); head.appendChild(close);
  document.addEventListener("keydown", e => { if (e.key === "Escape" && !w.hidden) { close.click(); } });
  w.appendChild(head);

  w.appendChild(el("div", "hint", T(cfg.hint)));
  const st = el("div", "cwstatus"); st.id = cfg.p + "status";
  w.appendChild(st);

  const forms = el("div", "cwforms");
  for (const side of ["board", "pc"]) { forms.appendChild(cfg.form(side)); }
  w.appendChild(forms);

  const bar = el("div", "cwbar");
  const sum = el("span", "cwsum"); sum.id = cfg.p + "sum";
  bar.appendChild(sum);
  const pause = el("button", null, T("canwin.pause"));
  pause.id = cfg.p + "pause";
  pause.onclick = () => { win.paused = !win.paused; pause.textContent = T(win.paused ? "canwin.resume" : "canwin.pause"); cfg.draw(); };
  const clear = el("button", null, T("canwin.clear"));
  clear.id = cfg.p + "clear";
  clear.onclick = () => act({ op: cfg.clear });
  const filt = el("input"); filt.id = cfg.p + "filter"; filt.placeholder = T(cfg.filter);
  filt.oninput = () => { win.filter = filt.value.trim(); cfg.draw(true); };
  bar.appendChild(pause); bar.appendChild(clear); bar.appendChild(filt);
  w.appendChild(bar);

  const tbl = el("div", "cwtable"); tbl.id = cfg.p + "table";
  w.appendChild(tbl);
  document.body.appendChild(w);
  return w;
}

// The card libLive draws for an analyser item on every redraw: the summary and
// the button that opens the window. kind is "can" or "knx"; the open button's
// data attribute is data-<kind>win-open.
function anaWinCard(win, kind, summary, draw, it, st) {
  const box = el("div", "live");
  box.dataset.live = it.id;
  const v = st.live[kind] || {};
  box.appendChild(el("div", "hint", T("canwin.card_hint")));
  box.appendChild(el("div", null, T(summary, { ok: v.matched || 0, miss: v.missing || 0, bad: v.bad || 0 })));
  const open = el("button", st.running ? "primary" : null, T("canwin.open"));
  open.dataset[kind + "winOpen"] = it.id;
  open.onclick = () => { win.shown = true; draw(true); };
  box.appendChild(open);
  if (st.running) {
    const end = el("button", null, T("lib.live.end"));
    end.dataset.liveend = it.id;
    end.onclick = () => liveAct(it.id, { op: "end" });
    box.appendChild(end);
  }
  win.id = it.id;
  win.st = st;
  // Opens by itself when the session comes up.
  if (st.running && !win.wasRunning) { win.shown = true; }
  win.wasRunning = !!st.running;
  draw();
  return box;
}

// The send form's labelled inputs; attr is the data attribute each input carries.
function anaWinField(f, attr, name, label, size, value) {
  const lab = el("label", null, T(label) + " ");
  const i = el("input"); i.dataset[attr] = name; i.size = size;
  if (value) { i.value = value; }
  lab.appendChild(i); f.appendChild(lab);
  return i;
}
