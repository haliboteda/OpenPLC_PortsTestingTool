package ptlib

import "time"

// The pairing the CAN and KNX analyser windows share: a frame one side sent
// waits for the other side to receive it, and a received frame nobody has
// claimed yet waits for its sender's report (the two arrive on separate
// streams, in either order). Two frames pair when key and body agree; same
// key with another body is a "bad" send.

type matchEntry struct {
	row       int // index into the window's rows, shifted down when rows drop off
	from      string
	key, body string
	shown     string // what a "bad" send reports the other side got
	src       uint16 // own sends only: the source the echo must carry
	deadline  time.Time
	got       string
}

type frameMatcher struct {
	wait    time.Duration // how long either half waits for the other
	pend    []matchEntry  // sends waiting for the other side
	orphans []matchEntry  // receptions waiting for their send
	own     []matchEntry  // sends the sender will also hear itself (KNX)
}

// expired is a send whose wait ran out: got is "" for missing, else bad.
type expired struct {
	row int
	got string
}

// sent registers a send at row; it returns the reception's row when one
// already waited for it.
func (m *frameMatcher) sent(row int, from, key, body string) (int, bool) {
	p := matchEntry{row: row, from: from, key: key, body: body, deadline: time.Now().Add(m.wait)}
	for k, o := range m.orphans {
		if o.from == from || o.key != key {
			continue
		}
		if o.body == body {
			m.orphans = append(m.orphans[:k], m.orphans[k+1:]...)
			return o.row, true
		}
		if p.got == "" {
			p.got = o.shown
		}
	}
	m.pend = append(m.pend, p)
	return 0, false
}

// received registers a reception at row; it returns the send's row when it
// answers one.
func (m *frameMatcher) received(row int, from, key, body, shown string) (int, bool) {
	for k, p := range m.pend {
		if p.from == from || p.key != key {
			continue
		}
		if p.body == body {
			m.pend = append(m.pend[:k], m.pend[k+1:]...)
			return p.row, true
		}
		if m.pend[k].got == "" {
			m.pend[k].got = shown
		}
	}
	m.orphans = append(m.orphans, matchEntry{row: row, from: from, key: key, body: body, shown: shown, deadline: time.Now().Add(m.wait)})
	return 0, false
}

// sentOwn remembers a send its sender will hear back on the same pair.
func (m *frameMatcher) sentOwn(row int, key, body string, src uint16) {
	m.own = append(m.own, matchEntry{row: row, key: key, body: body, src: src, deadline: time.Now().Add(m.wait)})
}

// heardOwn reports whether a reception is the sender's own send coming back,
// and forgets that send if so.
func (m *frameMatcher) heardOwn(key, body string, src uint16) bool {
	for k, s := range m.own {
		if s.key == key && s.body == body && s.src == src {
			m.own = append(m.own[:k], m.own[k+1:]...)
			return true
		}
	}
	return false
}

func (m *frameMatcher) waiting() bool { return len(m.pend) > 0 }

// expire lets go of everything whose wait ran out and returns the sends among them.
func (m *frameMatcher) expire(now time.Time) []expired {
	var out []expired
	keep := m.pend[:0]
	for _, p := range m.pend {
		if now.Before(p.deadline) {
			keep = append(keep, p)
			continue
		}
		out = append(out, expired{row: p.row, got: p.got})
	}
	m.pend = keep
	live := func(es []matchEntry) []matchEntry {
		k := es[:0]
		for _, e := range es {
			if now.Before(e.deadline) {
				k = append(k, e)
			}
		}
		return k
	}
	m.orphans, m.own = live(m.orphans), live(m.own)
	return out
}

// dropped follows the window letting go of its oldest `over` rows.
func (m *frameMatcher) dropped(over int) {
	shift := func(es []matchEntry) []matchEntry {
		k := es[:0]
		for _, e := range es {
			e.row -= over
			if e.row >= 0 {
				k = append(k, e)
			}
		}
		return k
	}
	m.pend, m.orphans, m.own = shift(m.pend), shift(m.orphans), shift(m.own)
}

func (m *frameMatcher) clear() { m.pend, m.orphans, m.own = nil, nil, nil }
