"use strict";

// The meter box's buttons, bound once: the box sits outside the redrawn cards.
$("#meterok").onclick    = () => meterDone($("#meterval").value.trim());
$("#meterskip").onclick  = () => meterDone("");
$("#meterstop").onclick  = () => meterDone(null);
$("#meterval").onkeydown = e => {
  if (e.key === "Enter") { meterDone($("#meterval").value.trim()); }
};


/* ---------------- go ---------------- */
// Page memo, then state, then the port list: each needs the one before for its first render (memo.js, last control port).
memoLoad()
  .then(() => refreshState())
  .then(() => loadPorts())
  .then(() => { if (state.connected) startEvents(); });
