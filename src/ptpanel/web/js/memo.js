"use strict";

// What the page remembers per test type, so switching port, item or test type,
// reloading, changing language or restarting the panel loses nothing
// (PORTTOOL-FLOW.md C.3.6 「页面记住什么」). The configurations keep theirs in
// /api/stagecfg; R&D and the full test's ticked steps are kept here, as flat
// cells in /api/memo, one section per test type.
//
// A value that fails its check stays in the cell, red, with the reason under
// it, and is never stored or sent to the board (「填数超量程」): it lives only
// in memoDraft until it is corrected.

let memoDraft = {};     // "<test type>#<cell key>" -> what was typed, while it is not acceptable
let memoTimer = null;
let memoLoaded = false;

/* ---------------- checks (the server repeats them) ---------------- */

const memoNum = v => (String(v).trim() === "" ? NaN : Number(v));

function memoItemOf(id) { return (catalog.items || []).find(it => it.id === id); }

// Why a value cannot be kept, in plain words; "" when it is fine. An empty
// cell is always fine: it clears.
function memoBad(key, v) {
  v = String(v).trim();
  if (v === "") { return ""; }
  const p = key.split("|");
  const range = id => { const it = memoItemOf(id); return it && typeof anaInfo === "function" ? anaInfo(it) : null; };
  const within = (lo, hi, unit, integer) => {
    const x = memoNum(v);
    if (!isFinite(x) || (integer && !Number.isInteger(x)) || x < lo || x > hi) {
      return T("memo.bad_range", { lo, hi, unit: unit || "" });
    }
    return "";
  };
  // Under the range's minimum (a 4-20 mA AO cannot go below about 4.86 mA,
  // calstore Range.Min) is said as such, in the server's words (decision 136).
  const floor = c => (typeof c.min === "number" ? Math.max(c.lo, c.min) : c.lo);
  const belowMin = (c, x) => (c && floor(c) > c.lo && isFinite(x) && x < floor(c) && x >= c.lo
    ? T("lib.reason.below_min", { min: floor(c) + " " + c.unit }) : "");
  switch (p[0]) {
    case "pt": case "tgt": {
      const c = range(p[1]);
      return c ? (belowMin(c, memoNum(v)) || within(floor(c), c.fs, c.unit)) : within(-Infinity, Infinity);
    }
    case "pts": {
      const c = range(p[1]);
      const xs = v.split(/[,，\s]+/).filter(x => x !== "");
      if (xs.length < 3 || xs.length > 6) { return T("ana.points_rule"); }
      for (const x of xs) {
        const n = memoNum(x);
        if (belowMin(c, n)) { return belowMin(c, n); }
        if (!isFinite(n) || (c && (n < floor(c) || n > c.fs))) { return c ? T("memo.bad_range", { lo: floor(c), hi: c.fs, unit: c.unit }) : T("ana.points_rule"); }
      }
      return "";
    }
    case "met": case "act": {
      const c = range(p[1]);
      return c ? within(0, Math.round(c.fs * 1.2 * 100) / 100, c.unit) : within(0, Infinity);
    }
    case "drop": return within(0, 30, "V");
    case "quad": return within(0, 1000000, "", true);
    case "blk": return within(0, 1000, "", true);
    case "par": {
      if (/[\s=]/.test(v)) { return T("memo.bad_param"); }
      // An AO load target is checked against its own AO's range (STG-38).
      const li = memoItemOf(p[1]);
      const lc = li && li.load ? loadChannelOf(p[2]) : "";
      if (lc && typeof anaState !== "undefined" && anaState && anaState.channels) {
        const c = anaState.channels[lc];
        const bad = c && (belowMin(c, memoNum(v)) || within(floor(c), c.fs, c.unit));
        if (bad) { return bad; }
      }
      // Against the limit the firmware states (pt.caps), each number of a
      // list or of a "channel:value" pair on its own.
      const it = memoItemOf(p[1]);
      const port = it && (state.ports || []).find(x => x.name === it.port);
      const l = port && port.limits && port.limits[p[2]];
      if (!l || (l.min == null && l.max == null)) { return ""; }
      for (const part of v.split(",")) {
        const x = memoNum(part.includes(":") ? part.split(":")[1] : part);
        if (!isFinite(x) || (l.min != null && x < l.min) || (l.max != null && x > l.max)) {
          return T("memo.bad_spec", { spec: l.spec });
        }
      }
      return "";
    }
    case "pwm": {
      const res = Number(dioPwm.res) || 8;
      if (p[2] === "duty") { return within(0, Math.pow(2, res) - 1, "", true); }
      if (p[2] === "freq") { return within(1, Math.floor(256000 / Math.pow(2, res)), "Hz", true); }
      return "";
    }
  }
  return "";
}

// A typed cell: shows the draft in red with its reason, stores only good values.
function memoCell(inp, key, get, set) {
  const wrap = el("span", "memocell");
  const hint = el("div", "cellhint");
  const mark = why => { inp.classList.toggle("badcell", !!why); hint.textContent = why; hint.hidden = !why; };
  const dk = view + "#" + key;
  const d = memoDraft[dk];
  inp.value = d !== undefined ? d : (get() ?? "");
  mark(d !== undefined ? memoBad(key, d) : "");
  inp.oninput = () => {
    const why = memoBad(key, inp.value);
    if (why) { memoDraft[dk] = inp.value; } else { delete memoDraft[dk]; set(inp.value.trim()); }
    mark(why);
  };
  wrap.appendChild(inp);
  wrap.appendChild(hint);
  return wrap;
}

// Anything still red blocks 开始 / 设置: nothing wrong is sent to the board.
function memoBlocked() {
  if (!Object.keys(memoDraft).some(k => k.startsWith(view + "#"))) { return false; }
  showMessage(T("memo.fix_first"), true);
  return true;
}

/* ---------------- what is kept ---------------- */

function memoCollect() {
  const rd = {}, full = {};
  const put = (k, v) => { if (v !== undefined && v !== null && String(v) !== "") { rd[k] = String(v); } };
  for (const [k, off] of Object.entries(itemOff)) { if (k.startsWith("rd|")) { rd["off|" + k.slice(3)] = off ? "1" : "0"; } }
  for (const [k, v] of Object.entries(itemPeer)) { if (k.startsWith("rd|")) { put("peer|" + k.slice(3), v); } }
  for (const [id, ps] of Object.entries(libEdits)) { for (const [k, v] of Object.entries(ps || {})) { put("par|" + id + "|" + k, v); } }
  put("dur|rd", duration.rd);
  for (const [id, t] of Object.entries(anaPtsText)) { put("pts|" + id, String(t).replace(/[，\s]+/g, ",").replace(/,+/g, ",").replace(/^,|,$/g, "")); }
  for (const [id, t] of Object.entries(anaTarget)) { put("tgt|" + id, t); }
  for (const [k, t] of Object.entries(anaTyped)) { put("met|" + k, t); }
  for (const [id, m] of Object.entries(anaMeas)) { if (m && id.startsWith("aout.")) { put("act|" + id, m.actual); } }
  for (const [k, v] of Object.entries(dioStd)) {
    const [blk, ch] = k.split("|");
    const cell = { level: "lvl", load: "drop", blink: "blk", pwmok: "pwmok" }[blk];
    if (cell) { put(cell + "|" + ch, v); }
  }
  for (const [k, on] of Object.entries(rdTicks)) { rd["chk|" + k] = on ? "1" : "0"; }
  for (const [k, snap] of Object.entries(rdSnap)) { put("snap|" + k, JSON.stringify(snap)); }
  for (const [k, v] of Object.entries(liveRefTyped)) { if (k.startsWith("din.quad|")) { put("quad|" + k.split("|")[1], v); } }
  put("pwmres", dioPwm.res);
  for (const [ch, r] of Object.entries(dioPwm.rows)) {
    rd["pwm|" + ch + "|on"] = r.on ? "1" : "0";
    put("pwm|" + ch + "|freq", r.freq); put("pwm|" + ch + "|duty", r.duty);
  }
  for (const [st, on] of Object.entries(fullPick)) { full["step|" + st] = on ? "1" : "0"; }
  return { rd, full };
}

function memoApply(f) {
  const rd = f.rd || {}, full = f.full || {};
  for (const [k, v] of Object.entries(rd)) {
    const p = k.split("|");
    switch (p[0]) {
      case "off": itemOff["rd|" + p[1]] = v === "1"; break;
      case "peer": if (v !== "ref") { itemPeer["rd|" + p[1]] = v; } break; // R&D has no reference board (decision 133)
      case "par": (libEdits[p[1]] = libEdits[p[1]] || {})[p[2]] = v; break;
      case "dur": if (p[1] === "rd") { duration.rd = v; } break;
      case "pts": anaPtsText[p[1]] = v; break;
      case "tgt": anaTarget[p[1]] = v; break;
      case "met": anaTyped[p[1] + "|" + p[2]] = v; break;
      case "act": (anaMeas[p[1]] = anaMeas[p[1]] || {}).actual = v; break;
      case "lvl": dioStd["level|" + p[1]] = v; break;
      case "drop": dioStd["load|" + p[1]] = v; break;
      case "blk": dioStd["blink|" + p[1]] = v; break;
      case "pwmok": dioStd["pwmok|" + p[1]] = v; break;
      case "chk": rdTicks[p[1] + "|" + p[2]] = v === "1"; break;
      case "snap": // snap|<UID>|<port>; one without a UID is ignored (STG-39)
        if (p.length === 3) { try { rdSnap[p[1] + "|" + p[2]] = JSON.parse(v); } catch (e) { /* a broken one is dropped */ } }
        break;
      case "quad": liveRefTyped["din.quad|" + p[1]] = v; break;
      case "pwmres": dioPwm.res = v; break;
      case "pwm": {
        const r = dioPwm.rows[p[1]] = dioPwm.rows[p[1]] || { on: false, freq: "1000", duty: "128" };
        if (p[2] === "on") { r.on = v === "1"; } else { r[p[2]] = v; }
        break;
      }
    }
  }
  for (const [k, v] of Object.entries(full)) { const p = k.split("|"); if (p[0] === "step") { fullPick[p[1]] = v === "1"; } }
}

async function memoLoad() {
  try { memoApply(await api("/api/memo")); } catch (e) { /* nothing remembered yet */ }
  memoLoaded = true;
}

async function memoSave() {
  memoTimer = null;
  if (!memoLoaded) { return; }
  const c = memoCollect();
  for (const section of ["rd", "full"]) {
    const j = await api("/api/memo", { section, cells: c[section] });
    if (j && j.error) { showMessage(j.error, true); }
  }
}

function memoSoon() { if (memoTimer) { clearTimeout(memoTimer); } memoTimer = setTimeout(memoSave, 600); }

// Every change on the page is a candidate: the handlers have updated the
// state by the time the timer fires.
for (const ev of ["change", "input", "click"]) { document.addEventListener(ev, memoSoon, true); }
