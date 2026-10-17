"use strict";

/* ---------------- names on screen ---------------- */
// The engineers' names, not terminal numbers (Hardware/Klemmenbezeichnungen-R.pdf, $PORTTOOL/CLAUDE.md).
const CHAN_NAME = tr({
  dout: "DO",  din: "DI",  ain: "AI",  aout: "AO",  relay: "Relay ",
  temp: "chan_name.temp",
});
const PORT_NAME = tr({
  rs485: "RS485", can: "CAN", rs232: "RS232", knx: "KNX",
  eth: "port_name.eth", usb: "USB", sd: "port_name.sd", sdram: "SDRAM", temp: "port_name.temp",
});
function displayName(p, i) {
  if (CHAN_NAME[p.name]) return CHAN_NAME[p.name] + i;
  if (p.channels === 1 && PORT_NAME[p.name]) return PORT_NAME[p.name];
  return p.name + i;
}

// What the screen shows for protocol names; the literals sent and received stay
// as they are and show on hover (decision 82, $PORTTOOL/CLAUDE.md).
const PARAM_CN = tr({
  ch: "param.ch", mode: "param.mode", period: "param.period",
  duty: "param.duty", freq: "param.freq", baud: "param.baud",
  res: "param.res",
  on: "param.on", mv: "param.mv",
  ip: "param.ip", port: "param.port",
  ga: "param.ga", src: "param.src", val: "param.val",
  // SDRAM hold time, >= 1000 ms: a shorter wait passes even with refresh off (PORTTOOL-FLOW.md C.3.6 sdram.hold).
  wait: "param.wait",
  minutes: "param.minutes",
  out1: "param.out1", out2: "param.out2",
});

// Enum values and reading values, as shown; what the board sends is unchanged.
const VALUE_CN = tr({
  hold: "value.hold", blink: "value.blink", square: "value.square",
  level: "value.level", quad: "value.quad",
  normal: "value.normal", listen: "value.listen", loopback: "value.loopback", extloop: "value.extloop",
  frames: "value.frames",
  // SDRAM hold session phases
  write: "value.write", wait: "value.wait", verify: "value.verify",
  echo: "value.echo", sink: "value.sink", source: "value.source", info: "value.info",
  mirror: "value.mirror",
  toggle: "value.toggle",
  raw: "value.raw", inv: "value.inv", ack: "value.ack", bad: "value.bad",
  ok: "value.ok", odd: "value.odd",
  dhcp: "value.dhcp", cfg: "value.cfg", unknown: "value.unknown", dead: "value.dead",
  lse: "value.lse", lsi: "value.lsi",
  none: "value.none", self: "value.self", ctrl: "value.ctrl", link: "value.link",
  tx: "value.tx", rx: "value.rx", gvwrite: "value.gvwrite",
  exfat: "exFAT", fat32: "FAT32", fat16: "FAT16",
});

// Frame fields shown on the cards; the log keeps the originals.
const FIELD_CN = tr({
  t: "field.t", seq: "field.seq", rx: "field.rx", miss: "field.miss",
  tick: "field.tick", mode: "field.mode", period: "field.period",
  duty: "field.duty", freq: "field.freq", baud: "field.baud",
  v: "field.v", vdda: "field.vdda", ok: "field.ok",
  rxlines: "field.rxlines", rxbytes: "field.rxbytes", junk: "field.junk", overrun: "field.overrun",
  recover_ms: "field.recover_ms", down_ms: "field.down_ms", lost: "field.lost", reset: "field.reset",
  bps: "field.bps", alive: "field.alive", tx: "field.tx", rx_frames: "field.rx_frames",
  tec: "field.tec", rec: "field.rec", lec: "field.lec",
  bus: "field.bus", vcc: "field.vcc", idle: "field.idle", pulses: "field.pulses",
  dropped: "field.dropped", chars: "field.chars", bad: "field.bad", mismatch: "field.mismatch",
  w_avg: "field.w_avg", d_avg: "field.d_avg", quiet_ms: "field.quiet_ms",
  ip: "field.ip", link: "field.link", conn: "field.conn", port: "field.port",
  rx_bytes: "field.rx_bytes", tx_bytes: "field.tx_bytes", kbps: "field.kbps",
  state: "field.state", enum: "field.enum", busy: "field.busy", follows: "field.follows", offset: "field.offset",
  base: "field.base", words_each: "field.words_each", bad_pattern: "field.bad_pattern", mib: "field.mib", v2x: "field.v2x",
  dir_low: "field.dir_low", tx_low: "field.tx_low", dir_high: "field.dir_high", tx_high: "field.tx_high",
  half_ms: "field.half_ms", pin: "field.pin", cause: "field.cause",
  detected: "field.detected", changes: "field.changes", in: "field.in", out: "field.out",
  elapsed_s: "field.elapsed_s", left_s: "field.left_s", cycles: "field.cycles",
  din: "field.din", faults: "field.faults",
  done: "field.done", tmin: "field.tmin", tmax: "field.tmax",
  g1: "field.g1", g2: "field.g2", g3: "field.g3", g4: "field.g4",
  d1: "field.d1", d2: "field.d2", d3: "field.d3", d4: "field.d4",
  moved: "field.moved", sent: "field.sent",
  found: "field.found", autoneg: "field.autoneg", speed: "field.speed", fd: "field.fd",
  fs: "field.fs", err: "field.err", size: "field.size", clk: "field.clk",
  crc: "field.crc", runs: "field.runs", mounted: "field.mounted",
  init: "field.init", ready: "field.ready", n: "field.n",
  // pt.run reply fields, where most criteria look
  id: "field.id", mdio_errors: "field.mdio_errors",
  databus: "field.databus", addrbus: "field.addrbus", patterns: "field.patterns",
  checked: "field.checked", failed: "field.failed", mismatches: "field.mismatches",
  wrote: "field.wrote", read_back: "field.read_back", identical: "field.identical",
  fresult: "field.fresult", passes: "field.passes", passed: "field.passed",
  first_bad_pass: "field.first_bad_pass", class: "field.class",
  first_bad: "field.first_bad", seed: "field.seed",
  phase: "field.phase", wait_ms: "field.wait_ms",
  ef1: "field.ef1", ef2: "field.ef2",
  // In echo mode the board does not send, so this, not seq/rx/miss, is the one to judge.
  replied: "field.replied",
  bytes: "field.bytes", block_size: "field.block_size", blocks: "field.blocks",
  read_bps: "field.read_bps", write_bps: "field.write_bps",
  read_ms: "field.read_ms", write_ms: "field.write_ms",
  verify_ms: "field.verify_ms", elapsed_ms: "field.elapsed_ms",
  time: "field.time", date: "field.date", uid: "field.uid", observed: "field.observed",
  _text: "field._text",
  // KNX telegram events
  dst: "field.dst", apci: "field.apci", raw: "field.raw", inv: "field.inv",
  crc_raw: "field.crc_raw", crc_inv: "field.crc_inv", crc_bad: "field.crc_bad",
  acks: "field.acks", ack_other: "field.ack_other", tx_frames: "field.tx_frames", rx_frames: "field.rx_frames.2",
  // eth / usb flood: drop is the board's, peer_* the PC's (PORTTOOL-FLOW.md B.9)
  drop: "field.drop",
  peer_sent: "field.peer_sent", peer_echoed: "field.peer_echoed",
  peer_lost: "field.peer_lost", peer_bad: "field.peer_bad", peer_kbps: "field.peer_kbps",
  peer_rtt_min_us: "field.peer_rtt_min_us", peer_rtt_avg_us: "field.peer_rtt_avg_us",
  peer_rtt_max_us: "field.peer_rtt_max_us",
  // Rates the executor derives from the board's counts (ptseq/rates.go, decision 151)
  down_kbps: "field.down_kbps", up_kbps: "field.up_kbps",
  write_mbps: "field.write_mbps", read_mbps: "field.read_mbps",
  // KNX diagnostic replies (PORTTOOL-FLOW.md A.3)
  shots: "field.shots", moved: "field.moved", edges: "field.edges",
  first_us: "field.first_us", width_us: "field.width_us",
  w_min: "field.w_min", w_max: "field.w_max", d_min: "field.d_min", d_max: "field.d_max",
  w_hist: "field.w_hist", d_hist: "field.d_hist",
  pu_tx: "field.pu_tx", pu_rx: "field.pu_rx", pu_miss: "field.pu_miss", pu_w_avg: "field.pu_w_avg",
  np_tx: "field.np_tx", np_rx: "field.np_rx", np_miss: "field.np_miss", np_w_avg: "field.np_w_avg",
});

// **x** prints bold and newlines break; no other markup, so no other escaping.
function richText(tag, cls, s) {
  const node = el(tag, cls);
  String(s).split("**").forEach((part, i) => {
    if (part === "") { return; }
    part.split("\n").forEach((line, k) => {
      if (k) { node.appendChild(el("br")); }
      if (line === "") { return; }
      node.appendChild(i % 2 ? el("b", null, line) : document.createTextNode(line));
    });
  });
  return node;
}

// A parameter's own help, after its title.
const PARAM_HELP = tr({
  dout: {duty: "param_help.dout.duty",
         freq: "param_help.dout.freq"},
  relay: {on: "param_help.relay.on"},
  aout: {mv: "param_help.aout.mv", out1: "param_help.aout.out1", out2: "param_help.aout.out2"},
  din:  {period: "param_help.din.period"},
  rs232: {baud: "param_help.rs232.baud"},
});

// A listed value that needs a word beside it in the dropdown.
const PARAM_VALUE_NOTE = tr({
  rs232: {baud: {"230400": "param_note.rs232.baud_230400"}},
});

function paramHelp(port, name) { return (PARAM_HELP[port] || {})[name] || ""; }

function paramCN(name) { return PARAM_CN[name] || name; }

// The AO an AO load target belongs to (ptitems.LoadChannel); "" for any other.
function loadChannelOf(k) { return { out1: "AO1", out2: "AO2" }[k] || ""; }

// A parameter's name; an AO load target adds its AO's registered unit (STG-38).
function paramLabel(it, k) {
  const ch = it.load ? loadChannelOf(k) : "";
  if (!ch) { return paramCN(k); }
  const c = typeof anaState !== "undefined" && anaState && anaState.channels ? anaState.channels[ch] : null;
  return paramCN(k) + " (" + (c && c.unit ? c.unit : "mA / mV") + ")";
}
function valueCN(v)    { return VALUE_CN[v] || v; }
function fieldCN(k)    { return FIELD_CN[k] || k; }

// A criterion as a sentence. ptcheck judges; this only words its structured
// result, because the CLI and CSV report keep Go's English (~/.claude/rules/language.md).
const OP_CN = tr({eq: "op.eq", ne: "op.ne", contains: "op.contains"});

function fieldLabel(port, field) {
  const m = /^([A-Za-z0-9_.]+)(?:\[(\d+)\])?$/.exec(String(field || ""));
  if (!m) { return String(field || ""); }
  const p = (state.ports || []).find(x => x.name === port);
  const ch = /^ch(\d+)$/.exec(m[1]);
  const base = (ch && p) ? displayName(p, Number(ch[1])) : fieldCN(m[1]);
  return m[2] === undefined ? base : (T("ui.fieldLabel.1", {base, m: (Number(m[2]) + 1)}));
}

// A person's number, "value" in the plan, named by what it is; step names the
// AI / AO channel ("ao2-out") when there are two.
const VALUE_BY_UNIT = { "% FS": "field.value.dev", V: "field.value.drop", count: "field.value.count" };
// The plan judges temperatures in tenths of a degree ("ch1[1]", unit 0.1C);
// the panel names the channel and shows °C.
const DECI_C = "0.1C";
const isDeciC = c => !!c && c.unit === DECI_C;
function checkGot(c, got) {
  const n = Number(got);
  return isDeciC(c) && got !== "" && isFinite(n) ? (n / 10).toFixed(1) + " °C" : valueCN(got);
}

function checkField(port, c, step) {
  if (isDeciC(c)) { return fieldLabel(port, String(c.field).replace(/\[\d+\]$/, "")); }
  if (c.field !== "value") { return fieldLabel(port, c.field); }
  const f = T(VALUE_BY_UNIT[c.unit] || "field.value");
  const m = /^(a[io])(\d)-/.exec(step || "");
  return m ? m[1].toUpperCase() + m[2] + " " + f : f;
}

function checkCN(port, c, step) {
  if (!c || !c.field) { return T("ui.checkCN.1"); }
  return checkCNOf(port, c, step) + (c.if_present ? T("ui.checkCN.if_present") : "");
}

function checkCNOf(port, c, step) {
  const f = checkField(port, c, step);
  if (isDeciC(c)) {
    const d = v => (v === undefined || v === null ? v : v / 10);
    c = Object.assign({}, c, { min: d(c.min), max: d(c.max), unit: "°C" });
  }
  const unit = c.unit ? " " + c.unit : "";
  const has = v => v !== undefined && v !== null;
  if (c.op === "range") {
    if (has(c.min) && has(c.max)) { return T("ui.checkCN.2", {f, min: c.min, max: c.max, unit}); }
    if (has(c.min)) { return T("ui.checkCN.3", {f, min: c.min, unit}); }
    return T("ui.checkCN.4", {f, max: c.max, unit});
  }
  if (c.op === "count_zero") { return T("ui.checkCN.5", {f}); }
  return f + " " + (OP_CN[c.op] || c.op) + " " + valueCN(c.value) + unit;
}

// A failed criterion as a sentence.
function failCN(port, x) {
  const c = x.check || {};
  const what = checkCN(port, c);
  if (x.missing) {
    return T("ui.failCN.1", {what, field: fieldLabel(port, c.field)});
  }
  return T("ui.failCN.got", {what, got: x.got === "" ? T("ui.failCN.3") : checkGot(c, x.got)});
}

const BOARD_LABEL = tr({
  bridge:   "board_label.bridge",
  upper:    "board_label.upper",
  lower:    "board_label.lower",
  junction: "board_label.junction",
  whole:    "board_label.whole",
});

