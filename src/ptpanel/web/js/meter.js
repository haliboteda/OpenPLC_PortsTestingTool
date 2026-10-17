"use strict";

// Shared by the calibration walks: the fixed meter box (#meterask), waiting
// for fresh frames, averaging AI raw readings and the regression plot.
// Used by analog.js (the AI / AO cards).
// PORTTOOL-FLOW.md C.3.2.

let meterResolve = null;   // resolves the open #meterask question

function askMeter(what, unit, hint) {
  return new Promise(resolve => {
    $("#meterwhat").textContent = what;
    $("#meterunit").textContent = unit;
    $("#meterhint").textContent = hint || "";
    const inp = $("#meterval");
    inp.value = "";
    $("#meterask").hidden = false;
    inp.focus();
    meterResolve = resolve;
  });
}

function meterDone(value) {
  const resolve = meterResolve;
  meterResolve = null;
  $("#meterask").hidden = true;
  if (resolve) resolve(value);
}

// Resolves once the port has pushed n frames NEWER than the ones already
// seen, so a caller can tell "the board answered my change" from "a frame
// from before it arrived".
function waitForNewFrames(port, n, ms) {
  const before = (reads[port] || {}).tick;
  return new Promise(resolve => {
    const fresh = new Set();
    const start = Date.now();
    const iv = setInterval(() => {
      const r = reads[port];
      if (r && r.tick !== undefined && r.tick !== before) { fresh.add(r.tick); }
      if (fresh.size >= n) { clearInterval(iv); resolve(true); return; }
      if (Date.now() - start > ms) { clearInterval(iv); resolve(false); }
    }, 60);
  });
}

// Regression plot: the measured points and the fitted line y = k·x + b; colours
// come from CSS variables so it follows the page theme.
function calPlot(pts, f, unit, xName, yName) {
  const NS = "http://www.w3.org/2000/svg";
  const W = 340, H = 210, L = 56, R = 12, TOP = 10, B = 40;
  const mk = (tag, attrs, text) => {
    const n = document.createElementNS(NS, tag);
    for (const [a, v] of Object.entries(attrs)) n.setAttribute(a, v);
    if (text !== undefined) n.textContent = text;
    return n;
  };
  const xs = pts.map(p => p.want), ys = pts.map(p => p.got);
  let x0 = Math.min(0, ...xs), x1 = Math.max(...xs);
  if (x1 === x0) x1 = x0 + 1;
  const line = x => f.gain * x + f.offset;
  let y0 = Math.min(...ys, line(x0), line(x1)), y1 = Math.max(...ys, line(x0), line(x1));
  if (y1 === y0) y1 = y0 + 1;
  const py = (y1 - y0) * 0.06; y0 -= py; y1 += py;
  const sx = x => L + (x - x0) / (x1 - x0) * (W - L - R);
  const sy = y => TOP + (y1 - y) / (y1 - y0) * (H - TOP - B);
  const fmt = v => Math.abs(v) >= 100 ? v.toFixed(0) : v.toFixed(2);

  const svg = mk("svg", {viewBox: "0 0 " + W + " " + H, class: "calplot", "data-cal": "plot",
                         role: "img", "aria-label": T("ui.fitBox.plot")});
  svg.appendChild(mk("line", {x1: L, y1: H - B, x2: W - R, y2: H - B, class: "ax"}));
  svg.appendChild(mk("line", {x1: L, y1: TOP, x2: L, y2: H - B, class: "ax"}));
  svg.appendChild(mk("text", {x: sx(x0), y: H - B + 13, "text-anchor": "start", class: "tk"}, fmt(x0)));
  svg.appendChild(mk("text", {x: sx(x1), y: H - B + 13, "text-anchor": "end", class: "tk"}, fmt(x1)));
  for (const v of [Math.min(...ys), Math.max(...ys)]) {
    svg.appendChild(mk("text", {x: L - 4, y: sy(v) + 4, "text-anchor": "end", class: "tk"}, fmt(v)));
  }
  svg.appendChild(mk("text", {x: (L + W - R) / 2, y: H - 6, "text-anchor": "middle", class: "lb"},
                     xName));
  svg.appendChild(mk("text", {x: 12, y: (TOP + H - B) / 2, "text-anchor": "middle", class: "lb",
                              transform: "rotate(-90 12 " + (TOP + H - B) / 2 + ")"}, yName));
  svg.appendChild(mk("line", {x1: sx(x0), y1: sy(line(x0)), x2: sx(x1), y2: sy(line(x1)),
                              class: "fit", "data-cal": "line"}));
  for (const p of pts) {
    svg.appendChild(mk("circle", {cx: sx(p.want), cy: sy(p.got), r: 4, class: "pt", "data-cal": "point"}));
  }
  return svg;
}

// Average of the raw reading over n frames newer than the call.
async function aicalSampleRaw(ch, n) {
  let sum = 0, got = 0;
  for (let i = 0; i < n; i++) {
    if (!await waitForNewFrames("ain", 1, 5000)) { break; }
    const v = String(((reads.ain || {}).fields || {})["ch" + ch] || "");
    const raw = Number(v.split("/")[0]);
    if (v.includes("/") && isFinite(raw)) { sum += raw; got++; }
  }
  return got ? sum / got : null;
}
