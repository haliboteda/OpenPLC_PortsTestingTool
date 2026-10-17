package ptseq

// Rates the executor works out from what the board reported, added to the
// attempt like peer statistics: recorded, not judged (decision 151,
// PORTTOOL-FLOW.md B.9 「上下行分开测」 and C.3.6 in $PROD).

import (
	"strconv"

	"PortTool/src/ptcheck"
	"PortTool/src/ptproto"
	"PortTool/src/ptreport"
)

// Field names of the derived rates.
const (
	RateDown    = "down_kbps"  // PC -> board, eth / usb mode=sink
	RateUp      = "up_kbps"    // board -> PC, eth / usb mode=source
	RateSDRAMWr = "write_mbps" // sdram.sweep
	RateSDRAMRd = "read_mbps"
)

// RateFields are the derived rates, in the order a card lists them.
var RateFields = []string{RateDown, RateUp, RateSDRAMWr, RateSDRAMRd}

// setRate is kbit/s of a byte counter between two frames, from the board's
// own counts: what the PC wrote into its buffers is more than what arrived.
func setRate(att *ptreport.Attempt, name string, first, last ptproto.Frame, counter string) {
	b0, ok0 := frameUint(first, counter)
	b1, ok1 := frameUint(last, counter)
	// t= is parsed into Tick, not Fields; uint32 subtraction survives the wrap.
	dt := uint64(last.Tick - first.Tick)
	if !ok0 || !ok1 || dt == 0 || b1 < b0 {
		return
	}
	// bytes * 8 / ms = kbit/s
	att.SetExtra(name, strconv.FormatUint((b1-b0)*8/dt, 10))
}

// setSweepRates is MB/s of the sweep's writes and reads: patterns x words x 4
// bytes over write_ms / verify_ms.
func setSweepRates(att *ptreport.Attempt, look ptcheck.Lookup) {
	u := func(k string) (uint64, bool) {
		v, ok := look(k)
		if !ok {
			return 0, false
		}
		n, err := strconv.ParseUint(v, 10, 64)
		return n, err == nil
	}
	pat, ok1 := u("patterns")
	words, ok2 := u("words_each")
	if !ok1 || !ok2 {
		return
	}
	bytes := float64(pat * words * 4)
	for _, x := range [][2]string{{RateSDRAMWr, "write_ms"}, {RateSDRAMRd, "verify_ms"}} {
		if ms, ok := u(x[1]); ok && ms > 0 {
			// bytes / ms / 1000 = MB/s
			att.SetExtra(x[0], strconv.FormatFloat(bytes/float64(ms)/1000, 'f', 1, 64))
		}
	}
}

func frameUint(f ptproto.Frame, k string) (uint64, bool) {
	v, ok := ptproto.Get(f.Fields, k)
	if !ok {
		return 0, false
	}
	n, err := strconv.ParseUint(v, 10, 64)
	return n, err == nil
}
