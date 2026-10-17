package ptlib

import (
	"fmt"
	"strings"
	"testing"

	"PortTool/src/ptproto"
)

// feed plays one reference DI level per frame, 50 ms apart, on channel 1.
func feed(c *chainCount, levels string, start uint32) {
	for i, l := range strings.Split(levels, "") {
		f, _ := ptproto.ParseFrame(fmt.Sprintf("din t=%d v=0x00 mode=level ch1=%s", start+uint32(i)*50, l))
		c.add(f)
	}
}

func newCount() *chainCount {
	return &chainCount{refOf: map[int]int{1: 1}, settle: 2, period: 50, level: map[int]string{}, cand: map[int]string{},
		held: map[int]int{}, falls: map[int]uint64{}, maxGap: 500, more: make(chan struct{}, 1)}
}

// A fall counts once the low level held its settle frames; a one-frame
// glitch or bounce at an edge does not count (decision 133: debounced).
func TestChainCountIsDebounced(t *testing.T) {
	for _, c := range []struct {
		levels string
		want   uint64
	}{
		{"0001111100000", 1},
		{"0001011100100011", 1}, // bounce on both edges, one cycle
		{"0001110111000", 1},    // one low frame while closed is not an open
		{"00011100", 1},
		{"000111000111000", 2},
		{"0001110", 0}, // the last open has not settled yet
	} {
		n := newCount()
		feed(n, c.levels, 1000)
		if got := n.count(1); got != c.want {
			t.Errorf("%s: counted %d, want %d", c.levels, got, c.want)
		}
	}
}

// A gap wider than half the shortest hold could hide a whole cycle.
func TestChainCountSaysWhenFramesGap(t *testing.T) {
	n := newCount()
	feed(n, "000", 1000)
	feed(n, "000", 2000)
	if n.gapErr == "" {
		t.Fatal("a 900 ms gap with 500 allowed went unnoticed")
	}
}
