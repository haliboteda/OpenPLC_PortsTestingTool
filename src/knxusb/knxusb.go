// Package knxusb drives a standard KNX USB interface (HID, KNX USB Transfer
// Protocol): the PC's end of the KNX analyser window (decision 151 #8,
// $PROD/docs/modules/M4/PORTTOOL-FLOW.md C.3.7 "上位机 KNX").
//
// This file is the byte layer only: HID report framing, the transfer
// protocol header, the Bus Access Server feature service, cEMI / EMI2 / EMI1
// L_Data frames and the TP1 telegram the board reports.
package knxusb

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// HID report layout.
const (
	ReportLen = 64   // whole report, report ID included
	ReportID  = 0x01 // the only report ID the protocol uses
	dataMax   = ReportLen - 3

	pktStart   = 0x01
	pktEnd     = 0x02
	pktPartial = 0x04
	seqMax     = 5 // sequence numbers 1..5; calimero HidReportHeader

	headerLen = 8
	// A start report carries at most 52 body bytes; calimero HidReport, knx-stack usb_tunnel_interface.cpp.
	startBodyMax = 52
)

// Transfer protocol IDs; calimero TransferProtocolHeader.
const (
	ProtoTunnel  = 0x01
	ProtoFeature = 0x0F
)

// EMI is the frame format the interface speaks on the tunnel.
type EMI byte

const (
	EMI1 EMI = 0x01
	EMI2 EMI = 0x02
	CEMI EMI = 0x03
)

func (e EMI) String() string {
	switch e {
	case EMI1:
		return "EMI1"
	case EMI2:
		return "EMI2"
	case CEMI:
		return "cEMI"
	}
	return fmt.Sprintf("EMI 0x%02X", byte(e))
}

// Bus Access Server services (Get 1 .. Info 4) and feature IDs (supported EMI 1,
// device descriptor 0 = 2, connection status 3, manufacturer 4, active EMI 5); calimero HidReport.
const (
	FeatGet      = 0x01
	FeatResponse = 0x02
	FeatSet      = 0x03
	FeatInfo     = 0x04

	FeatSupportedEMI = 0x01
	FeatBusStatus    = 0x03
	FeatActiveEMI    = 0x05
)

// L_Data message codes. cEMI and EMI2 share them; EMI1 differs on con / ind. knxd emi1.cpp / emi2.cpp / cemi.cpp.
const (
	codeReq      = 0x11
	codeCon      = 0x2E
	codeInd      = 0x29
	codeConEMI1  = 0x4E
	codeIndEMI1  = 0x49
	ctrlStandard = 0xBC // standard frame, do not repeat, broadcast, low priority
	hopDefault   = 6
)

// Telegram is one KNX L_Data frame, the same whichever side saw it.
type Telegram struct {
	Src   uint16
	Dst   uint16
	Group bool   // Dst is a group address
	TPDU  []byte // TPCI, APCI and data
}

// Packet is one transfer protocol message: header fields plus body.
type Packet struct {
	Proto byte
	ID    byte // EMI ID on the tunnel, service ID on the feature protocol
	Body  []byte
}

// EncodeReports splits a packet into HID reports of ReportLen bytes.
func EncodeReports(p Packet) [][]byte {
	data := make([]byte, 0, headerLen+len(p.Body))
	data = append(data, 0x00, headerLen, byte(len(p.Body)>>8), byte(len(p.Body)), p.Proto, p.ID, 0x00, 0x00)
	data = append(data, p.Body...)
	var out [][]byte
	for seq := 1; len(data) > 0; seq++ {
		n := min(len(data), dataMax)
		if seq == 1 {
			n = min(len(data), headerLen+startBodyMax)
		}
		kind := byte(pktPartial)
		if seq == 1 {
			kind = pktStart
		}
		if n == len(data) {
			kind |= pktEnd
		} else if seq == 1 {
			kind |= pktPartial
		}
		r := make([]byte, ReportLen)
		r[0], r[1], r[2] = ReportID, byte(seq<<4)|kind, byte(n)
		copy(r[3:], data[:n])
		out = append(out, r)
		data = data[n:]
	}
	return out
}

// Assembler joins HID reports back into packets.
type Assembler struct {
	buf []byte
	seq int // sequence number of the last report taken, 0 when idle
}

// Add takes one input report; it returns a packet once the report marked
// end arrived. A report that does not fit drops what was collected.
func (a *Assembler) Add(r []byte) (Packet, bool, error) {
	fail := func(err error) (Packet, bool, error) {
		a.buf, a.seq = nil, 0
		return Packet{}, false, err
	}
	if len(r) < 3 || r[0] != ReportID {
		return fail(errors.New("not a KNX USB report"))
	}
	seq, kind, n := int(r[1]>>4), r[1]&0x0F, int(r[2])
	if n > len(r)-3 || n > dataMax {
		return fail(fmt.Errorf("report says %d data bytes, carries %d", n, len(r)-3))
	}
	if kind&pktStart != 0 {
		if seq != 1 || n < headerLen {
			return fail(errors.New("start report out of sequence or shorter than the transfer header"))
		}
		a.buf = a.buf[:0]
	} else if a.seq == 0 || seq != a.seq+1 || seq > seqMax || kind&pktPartial == 0 {
		return fail(fmt.Errorf("report %d does not continue report %d", seq, a.seq))
	}
	a.seq = seq
	a.buf = append(a.buf, r[3:3+n]...)
	if kind&pktEnd == 0 {
		return Packet{}, false, nil
	}
	data, single := a.buf, seq == 1
	a.buf, a.seq = nil, 0
	if data[0] != 0x00 || data[1] != headerLen {
		return Packet{}, false, fmt.Errorf("transfer header version %d / length %d, want 0 / %d", data[0], data[1], headerLen)
	}
	body := data[headerLen:]
	// Multi-report bodies are taken whole: calimero and knx-stack put only the start report's share in the length field.
	if want := int(data[2])<<8 | int(data[3]); single {
		if want > len(body) {
			return Packet{}, false, fmt.Errorf("body of %d bytes, header wants %d", len(body), want)
		}
		body = body[:want]
	}
	return Packet{Proto: data[4], ID: data[5], Body: append([]byte(nil), body...)}, true, nil
}

// FeatureGet asks the interface for one feature.
func FeatureGet(feature byte) Packet {
	return Packet{Proto: ProtoFeature, ID: FeatGet, Body: []byte{feature}}
}

// FeatureSet sets one feature.
func FeatureSet(feature byte, value ...byte) Packet {
	return Packet{Proto: ProtoFeature, ID: FeatSet, Body: append([]byte{feature}, value...)}
}

// LinkLayerMode is what puts the interface in data link layer mode after
// the active EMI is set; without it an EMI1/EMI2 BCU passes no L_Data.
// knxd emi1.cpp / emi2.cpp / cemi.cpp, calimero KNXNetworkLinkUsb.linkLayerMode.
func LinkLayerMode(e EMI) [][]byte {
	switch e {
	case CEMI:
		// M_PropWrite.req cEMI server object, PID_COMM_MODE (52) = 0 data link layer.
		return [][]byte{{0xF6, 0x00, 0x08, 0x01, 0x34, 0x10, 0x01, 0x00}}
	case EMI2:
		// PEI_Switch.req to link layer.
		return [][]byte{{0xA9, 0x00, 0x18, 0x34, 0x56, 0x78, 0x0A}}
	case EMI1:
		// PC_Set_Value.req: clear the address table start (0x0116), then system state 0x0060 = 0x12 link layer.
		return [][]byte{{0x46, 0x01, 0x01, 0x16, 0x00}, {0x46, 0x01, 0x00, 0x60, 0x12}}
	}
	return nil
}

// SupportedEMI reads a supported-EMI response value (2 octets, bit 0 EMI1,
// bit 1 EMI2, bit 2 cEMI) and picks the best: cEMI, then EMI2, then EMI1.
func SupportedEMI(v []byte) (EMI, []EMI, bool) {
	if len(v) < 2 {
		return 0, nil, false
	}
	bits := uint16(v[0])<<8 | uint16(v[1])
	var all []EMI
	for _, e := range []EMI{CEMI, EMI2, EMI1} {
		if bits&(1<<(byte(e)-1)) != 0 {
			all = append(all, e)
		}
	}
	if len(all) == 0 {
		return 0, nil, false
	}
	return all[0], all, true
}

// EncodeReq builds the L_Data.req for t in the given EMI.
func EncodeReq(e EMI, t Telegram) ([]byte, error) {
	if len(t.TPDU) < 1 || len(t.TPDU) > 16 {
		return nil, fmt.Errorf("a standard frame carries 1 to 16 TPDU bytes, not %d", len(t.TPDU))
	}
	npci := byte(hopDefault<<4) | byte(len(t.TPDU)-1)
	if t.Group {
		npci |= 0x80
	}
	switch e {
	case CEMI:
		ctrl2 := byte(hopDefault << 4)
		if t.Group {
			ctrl2 |= 0x80
		}
		f := []byte{codeReq, 0x00, ctrlStandard, ctrl2, byte(t.Src >> 8), byte(t.Src), byte(t.Dst >> 8), byte(t.Dst), byte(len(t.TPDU) - 1)}
		return append(f, t.TPDU...), nil
	case EMI1, EMI2:
		// Control field: priority bits only; the interface fills the source.
		f := []byte{codeReq, ctrlStandard & 0x0C, byte(t.Src >> 8), byte(t.Src), byte(t.Dst >> 8), byte(t.Dst), npci}
		return append(f, t.TPDU...), nil
	}
	return nil, fmt.Errorf("unknown %s", e)
}

// Indication kinds.
const (
	KindInd = "ind"
	KindCon = "con"
)

// Event is one L_Data.ind or L_Data.con from the interface.
type Event struct {
	Kind string
	T    Telegram
	OK   bool // con: the frame went out acknowledged
}

// DecodeLData reads an EMI tunnel body. ok is false for message codes that
// are not L_Data.ind / L_Data.con.
func DecodeLData(e EMI, b []byte) (Event, bool, error) {
	if len(b) < 1 {
		return Event{}, false, errors.New("empty EMI frame")
	}
	var ev Event
	switch {
	case b[0] == codeInd && e != EMI1, b[0] == codeIndEMI1 && e == EMI1:
		ev.Kind = KindInd
	case b[0] == codeCon && e != EMI1, b[0] == codeConEMI1 && e == EMI1:
		ev.Kind = KindCon
	default:
		return Event{}, false, nil
	}
	var ctrl, lenByte byte
	var tpdu []byte
	if e == CEMI {
		if len(b) < 2 || len(b) < 2+int(b[1])+7 {
			return Event{}, false, errors.New("cEMI frame too short")
		}
		p := b[2+int(b[1]):]
		ctrl = p[0]
		ev.T.Group = p[1]&0x80 != 0
		ev.T.Src = uint16(p[2])<<8 | uint16(p[3])
		ev.T.Dst = uint16(p[4])<<8 | uint16(p[5])
		lenByte = p[6]
		tpdu = p[7:]
	} else {
		if len(b) < 7 {
			return Event{}, false, errors.New("EMI frame too short")
		}
		ctrl = b[1]
		ev.T.Src = uint16(b[2])<<8 | uint16(b[3])
		ev.T.Dst = uint16(b[4])<<8 | uint16(b[5])
		ev.T.Group = b[6]&0x80 != 0
		lenByte = b[6] & 0x0F
		tpdu = b[7:]
	}
	n := int(lenByte) + 1
	if len(tpdu) < n {
		return Event{}, false, fmt.Errorf("frame says %d TPDU bytes, carries %d", n, len(tpdu))
	}
	ev.T.TPDU = append([]byte(nil), tpdu[:n]...)
	ev.OK = ctrl&0x01 == 0
	return ev, true, nil
}

// ParseTP1 reads a TP1 standard frame as the board reports it (raw= octets,
// check octet last).
func ParseTP1(f []byte) (Telegram, error) {
	if len(f) < 8 {
		return Telegram{}, fmt.Errorf("%d octets is not a standard frame", len(f))
	}
	n := int(f[5]&0x0F) + 1
	if len(f) != 6+n+1 {
		return Telegram{}, fmt.Errorf("length field says %d TPDU octets, frame has %d", n, len(f)-7)
	}
	var x byte
	for _, b := range f {
		x ^= b
	}
	if x != 0xFF {
		return Telegram{}, errors.New("check octet does not match")
	}
	return Telegram{
		Src:   uint16(f[1])<<8 | uint16(f[2]),
		Dst:   uint16(f[3])<<8 | uint16(f[4]),
		Group: f[5]&0x80 != 0,
		TPDU:  append([]byte(nil), f[6:6+n]...),
	}, nil
}

// GroupWrite is a GroupValueWrite carrying a 6-bit value in the APCI octet.
func GroupWrite(src, ga uint16, value byte) Telegram {
	return Telegram{Src: src, Dst: ga, Group: true, TPDU: []byte{0x00, 0x80 | value&0x3F}}
}

// APCI names the group service: read, response, write, or "" for others.
func (t Telegram) APCI() string {
	if len(t.TPDU) < 2 {
		return ""
	}
	switch (uint16(t.TPDU[0]&0x03)<<8 | uint16(t.TPDU[1])) & 0x3C0 {
	case 0x000:
		return "read"
	case 0x040:
		return "response"
	case 0x080:
		return "write"
	}
	return ""
}

// Value renders the data: the 6-bit value of a short frame in decimal,
// longer data as hex bytes, "" for a read.
func (t Telegram) Value() string {
	switch {
	case len(t.TPDU) < 2 || t.APCI() == "read":
		return ""
	case len(t.TPDU) == 2:
		return strconv.Itoa(int(t.TPDU[1] & 0x3F))
	}
	parts := make([]string, len(t.TPDU)-2)
	for i, b := range t.TPDU[2:] {
		parts[i] = fmt.Sprintf("%02X", b)
	}
	return strings.Join(parts, " ")
}

// DstText is the destination as written in ETS.
func (t Telegram) DstText() string {
	if t.Group {
		return GAText(t.Dst)
	}
	return IAText(t.Dst)
}

// GAText writes a group address main/middle/sub.
func GAText(a uint16) string { return fmt.Sprintf("%d/%d/%d", a>>11, (a>>8)&0x07, a&0xFF) }

// IAText writes an individual address area.line.device.
func IAText(a uint16) string { return fmt.Sprintf("%d.%d.%d", a>>12, (a>>8)&0x0F, a&0xFF) }

// ParseGA reads main/middle/sub (0-31 / 0-7 / 0-255).
func ParseGA(s string) (uint16, error) {
	p, err := parse3(s, "/", 31, 7)
	if err != nil {
		return 0, errors.New("the group address is main/middle/sub, 0-31 / 0-7 / 0-255")
	}
	return uint16(p[0]<<11 | p[1]<<8 | p[2]), nil
}

// ParseIA reads area.line.device (0-15 . 0-15 . 0-255).
func ParseIA(s string) (uint16, error) {
	p, err := parse3(s, ".", 15, 15)
	if err != nil {
		return 0, errors.New("the individual address is area.line.device, 0-15 . 0-15 . 0-255")
	}
	return uint16(p[0]<<12 | p[1]<<8 | p[2]), nil
}

func parse3(s, sep string, hi, mid int) ([3]int, error) {
	var out [3]int
	parts := strings.Split(strings.TrimSpace(s), sep)
	if len(parts) != 3 {
		return out, errors.New("three parts")
	}
	for i, max := range []int{hi, mid, 255} {
		v, err := strconv.Atoi(parts[i])
		if err != nil || v < 0 || v > max {
			return out, errors.New("out of range")
		}
		out[i] = v
	}
	return out, nil
}
