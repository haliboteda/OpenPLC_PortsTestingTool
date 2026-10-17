package knxusb

import (
	"bytes"
	"errors"
	"testing"
	"time"
)

func TestFeatureGetReport(t *testing.T) {
	r := EncodeReports(FeatureGet(FeatSupportedEMI))
	if len(r) != 1 || len(r[0]) != ReportLen {
		t.Fatalf("got %d reports", len(r))
	}
	want := []byte{0x01, 0x13, 0x09, 0x00, 0x08, 0x00, 0x01, 0x0F, 0x01, 0x00, 0x00, 0x01}
	if !bytes.Equal(r[0][:len(want)], want) {
		t.Errorf("report % X, want % X", r[0][:len(want)], want)
	}
	for _, b := range r[0][len(want):] {
		if b != 0 {
			t.Fatalf("padding not zero: % X", r[0])
		}
	}
}

func TestLongPacketSplitsAndJoins(t *testing.T) {
	body := make([]byte, 100)
	for i := range body {
		body[i] = byte(i)
	}
	rs := EncodeReports(Packet{Proto: ProtoTunnel, ID: byte(CEMI), Body: body})
	if len(rs) != 2 || rs[0][1] != 0x15 || rs[1][1] != 0x26 || rs[0][2] != 60 || rs[1][2] != 108-60 {
		t.Fatalf("headers % X / % X", rs[0][:3], rs[1][:3])
	}
	var a Assembler
	if _, done, err := a.Add(rs[0]); done || err != nil {
		t.Fatalf("first report: done=%v err=%v", done, err)
	}
	p, done, err := a.Add(rs[1])
	if !done || err != nil || p.Proto != ProtoTunnel || p.ID != byte(CEMI) || !bytes.Equal(p.Body, body) {
		t.Fatalf("joined %+v done=%v err=%v", p, done, err)
	}
	// calimero and knx-stack put only the start report's share in the body length.
	rs[0][3+2], rs[0][3+3] = 0, 52
	a.Add(rs[0])
	if p, done, err := a.Add(rs[1]); !done || err != nil || !bytes.Equal(p.Body, body) {
		t.Fatalf("per-report body length: %+v done=%v err=%v", p, done, err)
	}
	// A continuation without its start, or out of sequence, is refused.
	if _, _, err := a.Add(rs[1]); err == nil {
		t.Error("a continuation without a start was taken")
	}
	bad := append([]byte(nil), rs[1]...)
	bad[1] = 0x36
	a.Add(rs[0])
	if _, _, err := a.Add(bad); err == nil {
		t.Error("report 3 accepted after report 1")
	}
}

func TestSupportedEMIPicksCEMIFirst(t *testing.T) {
	for _, c := range []struct {
		v    []byte
		want EMI
		ok   bool
	}{
		{[]byte{0x00, 0x07}, CEMI, true},
		{[]byte{0x00, 0x03}, EMI2, true},
		{[]byte{0x00, 0x01}, EMI1, true},
		{[]byte{0x00, 0x00}, 0, false},
		{[]byte{0x05}, 0, false},
	} {
		e, _, ok := SupportedEMI(c.v)
		if e != c.want || ok != c.ok {
			t.Errorf("% X: %v %v, want %v %v", c.v, e, ok, c.want, c.ok)
		}
	}
}

func TestReqEncoding(t *testing.T) {
	tg := GroupWrite(0, 0x0A03, 1) // 1/2/3
	c, _ := EncodeReq(CEMI, tg)
	if want := []byte{0x11, 0x00, 0xBC, 0xE0, 0x00, 0x00, 0x0A, 0x03, 0x01, 0x00, 0x81}; !bytes.Equal(c, want) {
		t.Errorf("cEMI % X, want % X", c, want)
	}
	e2, _ := EncodeReq(EMI2, tg)
	if want := []byte{0x11, 0x0C, 0x00, 0x00, 0x0A, 0x03, 0xE1, 0x00, 0x81}; !bytes.Equal(e2, want) {
		t.Errorf("EMI2 % X, want % X", e2, want)
	}
	if _, err := EncodeReq(CEMI, Telegram{Dst: 1}); err == nil {
		t.Error("an empty TPDU was accepted")
	}
}

func TestDecodeIndAndCon(t *testing.T) {
	ind := []byte{0x29, 0x00, 0xBC, 0xE0, 0x11, 0x05, 0x0A, 0x03, 0x01, 0x00, 0x80}
	ev, ok, err := DecodeLData(CEMI, ind)
	if !ok || err != nil || ev.Kind != KindInd || ev.T.Src != 0x1105 || ev.T.DstText() != "1/2/3" || ev.T.APCI() != "write" || ev.T.Value() != "0" {
		t.Fatalf("cEMI ind: %+v ok=%v err=%v", ev, ok, err)
	}
	// Additional info is skipped.
	conBad := []byte{0x2E, 0x02, 0xAA, 0xBB, 0xBD, 0xE0, 0x11, 0x05, 0x0A, 0x03, 0x01, 0x00, 0x81}
	ev, ok, _ = DecodeLData(CEMI, conBad)
	if !ok || ev.Kind != KindCon || ev.OK || ev.T.Value() != "1" {
		t.Fatalf("cEMI con with error bit: %+v", ev)
	}
	emi1 := []byte{0x49, 0x0C, 0x11, 0x05, 0x0A, 0x03, 0xE1, 0x00, 0x81}
	ev, ok, _ = DecodeLData(EMI1, emi1)
	if !ok || ev.Kind != KindInd || !ev.T.Group || ev.T.Value() != "1" {
		t.Fatalf("EMI1 ind: %+v", ev)
	}
	if _, ok, _ := DecodeLData(EMI2, emi1); ok {
		t.Error("0x49 read as an EMI2 L_Data")
	}
	if _, _, err := DecodeLData(CEMI, []byte{0x29, 0x00, 0xBC}); err == nil {
		t.Error("a short cEMI frame was read")
	}
}

func TestParseTP1MatchesTheBoardsFrame(t *testing.T) {
	// The board's own example ($BOOT porttool_knx.c header): 15.15.250 -> 1/0/1 write 1.
	f := []byte{0xBC, 0xFF, 0xFA, 0x08, 0x01, 0xE1, 0x00, 0x81, 0x2F}
	tg, err := ParseTP1(f)
	if err != nil || IAText(tg.Src) != "15.15.250" || tg.DstText() != "1/0/1" || tg.Value() != "1" {
		t.Fatalf("%+v %v", tg, err)
	}
	f[8] ^= 1
	if _, err := ParseTP1(f); err == nil {
		t.Error("a wrong check octet passed")
	}
}

func TestAddresses(t *testing.T) {
	if a, err := ParseGA("31/7/255"); err != nil || a != 0xFFFF {
		t.Errorf("31/7/255 = %04X %v", a, err)
	}
	if a, err := ParseIA("1.1.250"); err != nil || a != 0x11FA {
		t.Errorf("1.1.250 = %04X %v", a, err)
	}
	for _, s := range []string{"32/0/0", "1/8/0", "1/2", "a/b/c", "1/2/256"} {
		if _, err := ParseGA(s); err == nil {
			t.Errorf("%q accepted as a group address", s)
		}
	}
	if GAText(0x0A03) != "1/2/3" || IAText(0x11FA) != "1.1.250" {
		t.Error("address text")
	}
}

func TestDescriptorShape(t *testing.T) {
	// Usage page FFA0, report ID 1, 63 bytes in and out.
	d := []byte{0x06, 0xA0, 0xFF, 0x09, 0x01, 0xA1, 0x01, 0x85, 0x01,
		0x75, 0x08, 0x95, 0x3F, 0x09, 0x01, 0x81, 0x02, 0x09, 0x01, 0x91, 0x02, 0xC0}
	if s := parseDescriptor(d); !s.knxShaped() || s.page != 0xFFA0 {
		t.Errorf("KNX-shaped descriptor not recognised: %+v", s)
	}
	// A keyboard: generic desktop page.
	kb := []byte{0x05, 0x01, 0x09, 0x06, 0xA1, 0x01, 0x75, 0x01, 0x95, 0x08, 0x81, 0x02, 0xC0}
	if parseDescriptor(kb).knxShaped() {
		t.Error("a keyboard looks like a KNX interface")
	}
}

// fakeHID answers the feature queries like an interface supporting EMI1 and cEMI.
type fakeHID struct {
	in      chan []byte
	written [][]byte
	closed  bool
}

func (f *fakeHID) ReadReport() ([]byte, error) {
	r, ok := <-f.in
	if !ok {
		return nil, errors.New("closed")
	}
	return r, nil
}

func (f *fakeHID) WriteReport(r []byte) error {
	f.written = append(f.written, append([]byte(nil), r...))
	if r[3+4] == ProtoFeature && r[3+5] == FeatGet {
		var val []byte
		switch r[3+8] {
		case FeatSupportedEMI:
			val = []byte{0x00, 0x05}
		case FeatBusStatus:
			val = []byte{0x01}
		}
		resp := EncodeReports(Packet{Proto: ProtoFeature, ID: FeatResponse, Body: append([]byte{r[3+8]}, val...)})
		go func() { f.in <- resp[0] }()
	}
	return nil
}

func (f *fakeHID) Close() error {
	if !f.closed {
		f.closed = true
		close(f.in)
	}
	return nil
}

func TestOpenNegotiatesCEMI(t *testing.T) {
	f := &fakeHID{in: make(chan []byte, 8)}
	k, err := Open(f, "fake")
	if err != nil {
		t.Fatal(err)
	}
	defer k.Close()
	if k.EMI() != "cEMI" || k.BusUp() == nil || !*k.BusUp() {
		t.Fatalf("EMI %s bus %v", k.EMI(), k.BusUp())
	}
	set := f.written[1]
	if set[3+4] != ProtoFeature || set[3+5] != FeatSet || set[3+8] != FeatActiveEMI || set[3+9] != byte(CEMI) {
		t.Errorf("active-EMI set % X", set[:13])
	}
	ll := f.written[2]
	if want := []byte{0x01, 0x13, 0x10, 0x00, 0x08, 0x00, 0x08, 0x01, 0x03, 0x00, 0x00, 0xF6, 0x00, 0x08, 0x01, 0x34, 0x10, 0x01, 0x00}; !bytes.Equal(ll[:len(want)], want) {
		t.Errorf("link layer mode % X, want % X", ll[:len(want)], want)
	}
	ind := EncodeReports(Packet{Proto: ProtoTunnel, ID: byte(CEMI),
		Body: []byte{0x29, 0x00, 0xBC, 0xE0, 0x11, 0x05, 0x0A, 0x03, 0x01, 0x00, 0x81}})
	f.in <- ind[0]
	select {
	case ev := <-k.Events():
		if ev.Kind != KindInd || ev.T.DstText() != "1/2/3" {
			t.Errorf("event %+v", ev)
		}
	case <-time.After(time.Second):
		t.Fatal("no indication")
	}
}

func TestOpenRefusesASilentDevice(t *testing.T) {
	f := &silentHID{in: make(chan []byte)}
	if _, err := Open(f, "mute"); err == nil {
		t.Fatal("a device that never answers opened")
	}
}

type silentHID struct{ in chan []byte }

func (s *silentHID) ReadReport() ([]byte, error) {
	if _, ok := <-s.in; !ok {
		return nil, errors.New("closed")
	}
	return nil, nil
}
func (s *silentHID) WriteReport([]byte) error { return nil }
func (s *silentHID) Close() error             { close(s.in); return nil }
