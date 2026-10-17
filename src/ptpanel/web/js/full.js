"use strict";

/* ================= Full test page, below layer 2 =================
 *
 * Steps 2, 4, 5, 6 are ticked on layer 2 (flows.js) and run in order by the
 * server (src/ptstages); one record per board in records/<UID>/.
 * PORTTOOL-FLOW.md C.3.3, C.3.4. This draws the refusals, the "continue?"
 * question, the safety card and the last outcome, re-rendered only when the
 * server's state changes so the safety card's inputs are not wiped.
 */
const FULL_STEP = { 2: "ui.full.step2", 4: "ui.full.step4", 5: "ui.full.step5", 6: "ui.full.step6" };
const FULL_OUTCOME = { PASS: "go.full.o.pass", FAIL: "go.full.o.fail", TIMEOUT: "go.full.o.timeout",
  ERROR: "go.full.o.error", SKIPPED: "go.full.o.skipped", STOPPED: "go.full.o.stopped" };
let fullPick = {};            // step -> ticked
let fullAsk = null;           // the server's "continue from step N?" question
let fullNote = "";            // the last refusal
let fullCalPort = "";         // where 去研发测试 goes when the refusal is missing calibration
let fullData = null;          // the last /api/full, for the top-right button
let fullSafety = { sn: "", yes: [], notes: [], pass: null };
let fullSeen = "";            // the state last rendered
let fullTimer = null;

async function loadFull() {
  const j = await api("/api/full?log=" + runSince() + "&run=" + runId); // runview.js keeps the log
  const wasRunning = !!(fullData && fullData.running);
  fullData = j;
  if (j.ask) { libRefresh(); } // items that ask the person use the shared box (library.js)
  runTake(j);
  // The run's steps and log change every poll; they do not redraw the safety card.
  const key = JSON.stringify(Object.assign({}, j, { run: null })) + JSON.stringify(fullAsk) + fullNote;
  if (key !== fullSeen) {
    fullSeen = key;
    renderFull(j);
    if (wasRunning !== !!j.running && isFullSub(view)) { renderShell(); } else { renderFullTop(); } // flows.js
  }
  clearTimeout(fullTimer);
  if (isFullSub(view)) { fullTimer = setTimeout(loadFull, j.running ? 700 : 2000); }
}

async function startFull(resume) {
  const steps = fullTicked().map(s => s.step); // flows.js
  await rdLeaving;   // an R&D page's session ends first (rdvisit.js)
  // Cleared before the start, not after: a poll can show the safety card, and take
  // typing and a Submit, before this reply arrives.
  fullSafety = { sn: "", yes: [], notes: [], pass: null };
  fullNote = ""; fullCalPort = ""; fullAsk = null;
  const r = await api("/api/full/start", { steps, resume: resume || "" });
  if (r.error) { fullNote = r.error; }
  if (r.cal_port) { fullCalPort = r.cal_port; }
  if (r.ask) { fullAsk = r.ask; }
  loadFull();
}

async function stopFullRun() {
  await api("/api/full/stop", {});
  loadFull();
}

async function submitSafety(n) {
  const answers = [];
  for (let i = 0; i < n; i++) {
    answers.push({ yes: (i in fullSafety.yes) ? fullSafety.yes[i] : null, note: fullSafety.notes[i] || "" });
  }
  const r = await api("/api/full/safety", { sn: fullSafety.sn, answers, pass: fullSafety.pass });
  fullNote = r.error || "";
  loadFull();
}

function fullRadio(name, value, checked, onpick, label) {
  const lb = el("label");
  const r = el("input"); r.type = "radio"; r.name = "full-" + name; r.checked = checked;
  r.dataset.full = name + "=" + value;
  r.onchange = onpick;
  lb.appendChild(r);
  lb.appendChild(document.createTextNode(" " + label + " "));
  return lb;
}

function renderFull(j) {
  const host = $("#fullbody");
  if (!isFullSub(view)) { return; }
  host.textContent = "";
  if (j.last && j.last.root_left && !j.running) {
    host.appendChild(el("div", "msg bad", T("ui.full.root_left")));
  }

  if (fullNote) {
    const note = el("div", "msg bad", fullNote);
    note.dataset.full = "note";
    // Missing calibration: calibrate in R&D first (decision 97).
    if (fullCalPort) { note.appendChild(el("div", "hint", T("flow.cal_hint"))); note.appendChild(calButton(fullCalPort)); }
    host.appendChild(note);
  }

  if (fullAsk && !j.running) {
    const box = el("div", "msg");
    box.dataset.full = "ask";
    box.appendChild(el("div", null, T("ui.full.ask", { uid: fullAsk.uid, passed: fullAsk.passed.map(n => (FULL_STEP[n] ? T(FULL_STEP[n]) : n)).join(T("shell.list_sep")) })));
    const row = el("div", "row");
    const cont = el("button", "primary", T("ui.full.continue", { step: FULL_STEP[fullAsk.from] ? T(FULL_STEP[fullAsk.from]) : fullAsk.from }));
    cont.dataset.full = "continue";
    cont.onclick = () => startFull("continue");
    const over = el("button", null, T("ui.full.restart"));
    over.dataset.full = "restart";
    over.onclick = () => startFull("restart");
    const cancel = el("button", null, T("ui.full.cancel"));
    cancel.onclick = () => { fullAsk = null; loadFull(); };
    row.appendChild(cont); row.appendChild(over); row.appendChild(cancel);
    box.appendChild(row);
    host.appendChild(box);
  }

  if (j.safety) { host.appendChild(safetyCard(j.safety.questions || [])); }

  if (j.last && !j.running) {
    const box = el("div", "msg" + (j.last.passed ? "" : " bad"));
    box.dataset.full = "last";
    box.appendChild(el("div", null, T(j.last.passed ? "go.full.done_pass" : "go.full.done_fail")));
    box.appendChild(el("div", "sub", j.last.record ? T("ui.full.record", { path: j.last.record }) : j.last.record_why));
    host.appendChild(box);
  }
}

// Step 5 (STG-09): SN, one yes / no per question, the verdict, then Submit.
function safetyCard(questions) {
  const card = el("div", "msg");
  card.dataset.full = "safety";
  card.appendChild(el("b", null, T("ui.full.safety_title")));
  const sn = el("input");
  sn.dataset.full = "sn";
  sn.placeholder = T("ui.full.sn");
  sn.value = fullSafety.sn;
  sn.style.width = "260px";
  sn.oninput = () => { fullSafety.sn = sn.value; };
  const snrow = el("div", "row");
  snrow.appendChild(sn);
  card.appendChild(snrow);
  if (!questions.length) { card.appendChild(el("div", "hint", T("ui.full.no_questions"))); }
  questions.forEach((q, i) => {
    const row = el("div", "row");
    row.appendChild(el("span", null, (i + 1) + ". " + q));
    row.appendChild(fullRadio("q" + i, "yes", fullSafety.yes[i] === true, () => { fullSafety.yes[i] = true; }, T("ui.full.yes")));
    row.appendChild(fullRadio("q" + i, "no", fullSafety.yes[i] === false, () => { fullSafety.yes[i] = false; }, T("ui.full.no")));
    const note = el("input");
    note.placeholder = T("ui.full.note");
    note.value = fullSafety.notes[i] || "";
    note.oninput = () => { fullSafety.notes[i] = note.value; };
    row.appendChild(note);
    card.appendChild(row);
  });
  const v = el("div", "row");
  v.appendChild(el("span", null, T("ui.full.verdict")));
  v.appendChild(fullRadio("verdict", "pass", fullSafety.pass === true, () => { fullSafety.pass = true; }, T("ui.full.pass")));
  v.appendChild(fullRadio("verdict", "fail", fullSafety.pass === false, () => { fullSafety.pass = false; }, T("ui.full.fail")));
  card.appendChild(v);
  const submit = el("button", "primary", T("ui.full.submit"));
  submit.dataset.full = "submit";
  submit.onclick = () => submitSafety(questions.length);
  card.appendChild(submit);
  return card;
}
