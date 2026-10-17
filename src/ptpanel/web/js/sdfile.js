"use strict";

// The SD file transfer block under the R&D SD port (decision 120,
// PORTTOOL-FLOW.md C.3.6 「SD 文件传递」, protocol B.11): write a file from this
// machine onto the card and have it read back, list the card's PORTTOOL
// folder, view / save / delete a file, eject. The board judges nothing; the
// two rows (bytes written, CRC read back) are compared here.

let sdfVia = "eth";
let sdfPut = null;      // the last write's result
let sdfFiles = null;    // the last listing
let sdfView = null;     // {name, ...} the file being shown
let sdfBusy = "";       // what is running: "put", "ls", "get:<name>", ...
let sdfPick = null;     // the File chosen on this machine
let sdfName = "";

const SDF_VIA = { eth: "sdf.via.eth", usb: "sdf.via.usb" };
const SDF_NAME_OK = /^[\x20-\x7e]{1,64}$/;
const SDF_NAME_BAD = /[\/\\:*?"<>|]/;

function sdfNameOk(n) { return SDF_NAME_OK.test(n) && !SDF_NAME_BAD.test(n) && n !== "." && n !== ".."; }

function sdfSize(n) {
  if (n >= 1048576) { return (n / 1048576).toFixed(1) + " MB"; }
  if (n >= 1024) { return (n / 1024).toFixed(1) + " KB"; }
  return n + " B";
}

async function sdfCall(what, path, opt) {
  sdfBusy = what; renderRight();
  try {
    const r = await fetch(path, opt || {});
    if ((r.headers.get("Content-Type") || "").startsWith("application/octet-stream")) { return { blob: await r.blob() }; }
    return localize(await r.json().catch(() => ({ error: T("ui.api.1") })));
  } finally { sdfBusy = ""; renderRight(); }
}

function sdfPanel() {
  const card = el("div", "item anacard sdfcard"); card.dataset.sdfile = "1";
  const head = el("div", "ih");
  head.appendChild(el("b", null, T("sdf.title")));
  head.appendChild(el("span", "sp"));
  const lb = el("label", null, T("sdf.via") + " ");
  const sel = el("select"); sel.dataset.sdf = "via";
  for (const v of ["eth", "usb"]) { const o = el("option", null, T(SDF_VIA[v])); o.value = v; o.selected = v === sdfVia; sel.appendChild(o); }
  sel.onchange = () => { sdfVia = sel.value; };
  sel.disabled = !!sdfBusy;
  lb.appendChild(sel); head.appendChild(lb);
  card.appendChild(head);
  card.appendChild(sdfPutBlock());
  card.appendChild(sdfListBlock());
  return card;
}

function sdfPutBlock() {
  const blk = el("div", "anablk");
  const kh = el("div", "khead");
  kh.appendChild(el("b", null, T("sdf.put.title")));
  blk.appendChild(kh);
  blk.appendChild(el("div", "hint", T("sdf.put.what")));

  const row = el("div", "row");
  row.appendChild(el("span", null, T("sdf.put.file")));
  const file = el("input"); file.type = "file"; file.dataset.sdf = "file";
  file.onchange = () => { sdfPick = file.files[0] || null; if (sdfPick && !sdfName) { sdfName = sdfPick.name; } renderRight(); };
  row.appendChild(file);
  if (sdfPick) { row.appendChild(el("span", "hint", sdfPick.name + ", " + sdfSize(sdfPick.size))); }
  row.appendChild(el("span", null, T("sdf.put.name")));
  const name = el("input"); name.dataset.sdf = "name"; name.value = sdfName; name.style.width = "180px";
  name.oninput = () => { sdfName = name.value; };
  name.onchange = () => renderRight();
  row.appendChild(name);
  const go = el("button", "primary", T("sdf.put.go")); go.dataset.sdf = "put";
  go.disabled = !!sdfBusy || !sdfPick;
  go.onclick = sdfDoPut;
  row.appendChild(go);
  blk.appendChild(row);
  if (sdfName && !sdfNameOk(sdfName)) { blk.appendChild(el("div", "hint bad", T("sdf.name_rule"))); }
  if (sdfBusy === "put") { blk.appendChild(el("div", "hint", T("sdf.put.running", { via: T(SDF_VIA[sdfVia]) }))); }

  const r = sdfPut;
  if (r && !r.error) {
    const tbl = el("table", "gtab crit");
    const hr = el("tr");
    for (const h of [T("shell.item.col.item"), T("shell.item.col.want"), T("shell.item.col.got")]) { hr.appendChild(el("th", null, h)); }
    tbl.appendChild(hr);
    const line = (what, want, got, ok) => {
      const tr = el("tr", ok ? "ok" : "bad");
      tr.appendChild(el("td", null, what)); tr.appendChild(el("td", null, want));
      tr.appendChild(el("td", null, (ok ? "✅ " : "❌ ") + got));
      tbl.appendChild(tr);
    };
    line(T("sdf.row.bytes"), String(r.bytes), String(r.wrote), r.wrote === r.bytes);
    line(T("sdf.row.crc"), T("sdf.crc.pc", { crc: r.want_crc }), T("sdf.crc.board", { crc: r.got_crc }), r.want_crc === r.got_crc);
    blk.appendChild(tbl);
    blk.appendChild(el("div", "hint", T("sdf.put.rate", { kbps: r.kbps.toFixed(1), via: T(SDF_VIA[r.via]) })));
  } else if (r && r.error) {
    blk.appendChild(el("div", "hint bad", r.error));
  }
  return blk;
}

async function sdfDoPut() {
  if (!sdfPick) { return; }
  if (!sdfNameOk(sdfName)) { showMessage(T("sdf.name_rule"), true); return; }
  const body = await sdfPick.arrayBuffer();
  const q = "via=" + encodeURIComponent(sdfVia) + "&name=" + encodeURIComponent(sdfName);
  sdfPut = await sdfCall("put", "/api/sd/put?" + q, { method: "POST", body });
  if (!sdfPut.error) { sdfList(); }
}

function sdfListBlock() {
  const blk = el("div", "anablk");
  const kh = el("div", "khead");
  kh.appendChild(el("b", null, T("sdf.ls.title")));
  kh.appendChild(el("span", "sp"));
  const refresh = el("button", null, T("sdf.ls.refresh")); refresh.dataset.sdf = "ls";
  refresh.disabled = !!sdfBusy; refresh.onclick = sdfList;
  kh.appendChild(refresh);
  const eject = el("button", null, T("sdf.eject")); eject.dataset.sdf = "eject";
  eject.disabled = !!sdfBusy; eject.onclick = sdfEject;
  kh.appendChild(eject);
  blk.appendChild(kh);
  blk.appendChild(el("div", "hint", T("sdf.ls.what")));

  if (sdfFiles && sdfFiles.error) { blk.appendChild(el("div", "hint bad", sdfFiles.error)); return blk; }
  if (sdfFiles && sdfFiles.ejected) { blk.appendChild(el("div", "hint", T("sdf.ejected"))); }
  const files = (sdfFiles && sdfFiles.files) || null;
  if (files && !files.length) { blk.appendChild(el("div", "hint", T("sdf.ls.empty"))); }
  if (files && files.length) {
    const tbl = el("table", "gtab");
    const hr = el("tr");
    for (const h of [T("sdf.col.name"), T("sdf.col.size"), T("sdf.col.time"), T("sdf.col.act")]) { hr.appendChild(el("th", null, h)); }
    tbl.appendChild(hr);
    for (const f of files) {
      const tr = el("tr"); tr.dataset.sdfile = f.name;
      tr.appendChild(el("td", null, f.name));
      tr.appendChild(el("td", null, sdfSize(f.size)));
      tr.appendChild(el("td", null, f.date + " " + f.time));
      const td = el("td");
      const view = el("button", null, T("sdf.view")); view.disabled = !!sdfBusy; view.onclick = () => sdfShow(f.name);
      const save = el("button", null, T("sdf.save")); save.disabled = !!sdfBusy; save.onclick = () => sdfSave(f.name);
      const del = el("button", null, T("sdf.rm")); del.disabled = !!sdfBusy; del.onclick = () => sdfRemove(f.name);
      td.appendChild(view); td.appendChild(save); td.appendChild(del);
      tr.appendChild(td);
      tbl.appendChild(tr);
    }
    blk.appendChild(tbl);
  }
  if (sdfBusy.startsWith("get:")) { blk.appendChild(el("div", "hint", T("sdf.get.running", { name: sdfBusy.slice(4) }))); }
  const v = sdfView;
  if (v) {
    if (v.error) { blk.appendChild(el("div", "hint bad", v.error)); }
    else {
      const ok = v.board_crc === v.got_crc;
      blk.appendChild(el("div", "hint " + (ok ? "" : "bad"),
        T(ok ? "sdf.view.ok" : "sdf.view.bad", { name: v.name, size: sdfSize(v.size), crc: v.board_crc, got: v.got_crc })));
      const pre = el("pre", "mono events", v.text !== undefined ? v.text : v.hex);
      pre.dataset.sdfview = v.name;
      blk.appendChild(pre);
      if (v.truncated) { blk.appendChild(el("div", "hint", T("sdf.view.cut"))); }
    }
  }
  return blk;
}

async function sdfList() { sdfFiles = await sdfCall("ls", "/api/sd/ls"); }

async function sdfShow(name) {
  const j = await sdfCall("get:" + name, "/api/sd/get?via=" + encodeURIComponent(sdfVia) + "&name=" + encodeURIComponent(name));
  sdfView = Object.assign({ name }, j);
}

async function sdfSave(name) {
  const j = await sdfCall("get:" + name, "/api/sd/get?download=1&via=" + encodeURIComponent(sdfVia) + "&name=" + encodeURIComponent(name));
  if (j.error) { showMessage(j.error, true); return; }
  const a = el("a"); a.href = URL.createObjectURL(j.blob); a.download = name;
  document.body.appendChild(a); a.click(); a.remove();
  setTimeout(() => URL.revokeObjectURL(a.href), 10000);
}

async function sdfRemove(name) {
  if (!confirm(T("sdf.rm.confirm", { name }))) { return; }
  const j = await sdfCall("rm", "/api/sd/rm?name=" + encodeURIComponent(name), { method: "POST" });
  if (j.error) { showMessage(j.error, true); return; }
  if (sdfView && sdfView.name === name) { sdfView = null; }
  sdfList();
}

async function sdfEject() {
  const j = await sdfCall("eject", "/api/sd/eject", { method: "POST" });
  if (j.error) { showMessage(j.error, true); return; }
  sdfFiles = { ejected: true };
  sdfView = null;
}
