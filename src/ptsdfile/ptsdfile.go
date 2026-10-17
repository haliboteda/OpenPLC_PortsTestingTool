// Package ptsdfile moves files between this machine and the board's SD card:
// the control port carries the pt.run sd.* commands and the results, the
// data rides the eth or usb session in mode=file. PORTTOOL-FLOW.md B.11,
// DECISIONS.md 120.
package ptsdfile

import (
	"bufio"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"strconv"
	"strings"
	"time"

	"PortTool/src/ptboard"
	"PortTool/src/ptproto"
)

// Chunk is the most this end sends before the board's ack, the board's
// SDF_CHUNK: USB has no back-pressure, so the PC paces the write.
const Chunk = 4096

// Wait bounds every step that waits for the board: an ack, the result line,
// the next bytes of a read. The board itself gives up after 10 s of silence.
const Wait = 15 * time.Second

const settle = 300 * time.Millisecond

// Opener opens the data channel once the board's session is listening.
type Opener func() (io.ReadWriteCloser, error)

// Error is a refusal or failure the board reported, by its error= code.
type Error struct {
	Code   string // name, size, nocard, open, write, readback, timeout, ...
	Detail string
}

func (e *Error) Error() string {
	if e.Detail != "" {
		return "sd " + e.Code + ": " + e.Detail
	}
	return "sd " + e.Code
}

// File is one entry of the card's PORTTOOL folder.
type File struct {
	Name string `json:"name"`
	Size int64  `json:"size"`
	Date string `json:"date"`
	Time string `json:"time"`
}

// PutResult is a finished write: what was sent against what the board read
// back from the card.
type PutResult struct {
	Bytes   int           `json:"bytes"`
	Wrote   int64         `json:"wrote"`
	WantCRC uint32        `json:"want_crc"`
	GotCRC  uint32        `json:"got_crc"`
	Elapsed time.Duration `json:"elapsed_ms"`
}

// OK is the verdict: every byte written and the card's CRC is this machine's.
func (r PutResult) OK() bool { return r.Wrote == int64(r.Bytes) && r.WantCRC == r.GotCRC }

// GetResult is a finished read.
type GetResult struct {
	Size     int64  `json:"size"`
	BoardCRC uint32 `json:"board_crc"`
	GotCRC   uint32 `json:"got_crc"`
}

// OK is the verdict: the bytes that arrived have the CRC the board computed.
func (r GetResult) OK() bool { return r.BoardCRC == r.GotCRC }

func fields(line string) map[string]string {
	out := map[string]string{}
	for _, f := range strings.Fields(line) {
		if k, v, ok := strings.Cut(f, "="); ok {
			out[k] = v
		}
	}
	return out
}

func hex32(s string) uint32 {
	v, _ := strconv.ParseUint(strings.TrimPrefix(strings.ToLower(s), "0x"), 16, 32)
	return uint32(v)
}

// run sends one pt.run sd.* line and returns its fields, or the board's error.
func run(b *ptboard.Board, cmd string) (map[string]string, error) {
	lines, err := b.Send("pt.run "+cmd, ptboard.ExpectOne, ptboard.RunTimeout)
	if err != nil {
		return nil, err
	}
	f := fields(lines[0])
	if f["ok"] != "1" {
		code := f["error"]
		if code == "" {
			code = "refused"
		}
		return f, &Error{Code: code, Detail: lines[0]}
	}
	return f, nil
}

// session starts via's session in mode=file and opens the data channel; the
// returned func undoes both.
func session(b *ptboard.Board, via string, open Opener) (io.ReadWriteCloser, func(), error) {
	if via != "eth" && via != "usb" {
		return nil, nil, fmt.Errorf("via=%q is not eth or usb", via)
	}
	_, _ = b.Send("pt.stop "+via, ptboard.ExpectOne, ptboard.DefaultTimeout)
	if _, err := b.Send("pt.start "+via+" mode=file", ptboard.ExpectOne, ptboard.DefaultTimeout); err != nil {
		return nil, nil, err
	}
	stop := func() { _, _ = b.Send("pt.stop "+via, ptboard.ExpectOne, ptboard.DefaultTimeout) }
	ch, err := open()
	if err != nil {
		stop()
		return nil, nil, err
	}
	// The board takes the connection on its next pass; a transfer armed
	// before that would answer a peer that is not this one.
	time.Sleep(settle)
	return ch, func() { _ = ch.Close(); stop() }, nil
}

// lines feeds the data channel's lines to a channel until it closes.
func lines(r io.Reader) <-chan string {
	out := make(chan string, 64)
	go func() {
		defer close(out)
		sc := bufio.NewScanner(r)
		for sc.Scan() {
			out <- strings.TrimSpace(sc.Text())
		}
	}()
	return out
}

// result waits for the !sd.<what> event naming name.
func result(events <-chan ptboard.Event, what, name string) (map[string]string, error) {
	timer := time.NewTimer(Wait)
	defer timer.Stop()
	for {
		select {
		case ev, ok := <-events:
			if !ok {
				return nil, errors.New("the board stopped answering")
			}
			if ev.Kind != ptproto.LineFrame || ev.Frame.Port != what {
				continue
			}
			f := fields(ev.Line)
			if f["name"] != name {
				continue
			}
			if f["ok"] != "1" {
				return f, &Error{Code: f["error"], Detail: ev.Line}
			}
			return f, nil
		case <-timer.C:
			return nil, &Error{Code: "timeout"}
		}
	}
}

// Put writes data to PORTTOOL/name on the card over via and has the board
// read it back. progress, if set, is told the bytes acknowledged so far.
func Put(b *ptboard.Board, via string, open Opener, name string, data []byte, progress func(int)) (PutResult, error) {
	res := PutResult{Bytes: len(data), WantCRC: crc32.ChecksumIEEE(data)}
	t0 := time.Now()
	ch, done, err := session(b, via, open)
	if err != nil {
		return res, err
	}
	defer done()
	events, unsub := b.Subscribe(256)
	defer unsub()
	if _, err := run(b, fmt.Sprintf("sd.put name=%s size=%d", name, len(data))); err != nil {
		return res, err
	}
	acks := lines(ch)
	sent := 0
	for sent < len(data) {
		n := min(Chunk, len(data)-sent)
		if _, err := ch.Write(data[sent : sent+n]); err != nil {
			return res, err
		}
		sent += n
		if err := waitAck(acks, sent); err != nil {
			return res, err
		}
		if progress != nil {
			progress(sent)
		}
	}
	f, err := result(events, "sd.put", name)
	if err != nil {
		return res, err
	}
	res.Wrote, _ = strconv.ParseInt(f["wrote"], 10, 64)
	res.GotCRC = hex32(f["crc"])
	res.Elapsed = time.Since(t0)
	return res, nil
}

// waitAck reads "A<total>" lines until the board has written at least want.
func waitAck(acks <-chan string, want int) error {
	timer := time.NewTimer(Wait)
	defer timer.Stop()
	for {
		select {
		case l, ok := <-acks:
			if !ok {
				return errors.New("the data line closed during the write")
			}
			if n, err := strconv.Atoi(strings.TrimPrefix(l, "A")); err == nil && strings.HasPrefix(l, "A") && n >= want {
				return nil
			}
		case <-timer.C:
			return &Error{Code: "timeout", Detail: fmt.Sprintf("no ack for %d bytes", want)}
		}
	}
}

// Get reads PORTTOOL/name back over via.
func Get(b *ptboard.Board, via string, open Opener, name string) ([]byte, GetResult, error) {
	var res GetResult
	ch, done, err := session(b, via, open)
	if err != nil {
		return nil, res, err
	}
	defer done()
	events, unsub := b.Subscribe(256)
	defer unsub()
	f, err := run(b, "sd.get name="+name)
	if err != nil {
		return nil, res, err
	}
	res.Size, _ = strconv.ParseInt(f["size"], 10, 64)
	res.BoardCRC = hex32(f["crc"])
	data := make([]byte, res.Size)
	got := make(chan error, 1)
	go func() { _, err := io.ReadFull(ch, data); got <- err }()
	select {
	case err := <-got:
		if err != nil {
			return nil, res, err
		}
	case <-time.After(Wait + time.Duration(res.Size/20000)*time.Second):
		return nil, res, &Error{Code: "timeout", Detail: "the file did not arrive"}
	}
	res.GotCRC = crc32.ChecksumIEEE(data)
	if _, err := result(events, "sd.get", name); err != nil {
		return data, res, err
	}
	return data, res, nil
}

// List reads the card's PORTTOOL folder.
func List(b *ptboard.Board) ([]File, error) {
	events, unsub := b.Subscribe(512)
	defer unsub()
	f, err := run(b, "sd.ls")
	if err != nil {
		return nil, err
	}
	count, _ := strconv.Atoi(f["count"])
	files := []File{}
	timer := time.NewTimer(time.Second)
	defer timer.Stop()
	for len(files) < count {
		select {
		case ev, ok := <-events:
			if !ok {
				return files, errors.New("the board stopped answering")
			}
			if ev.Kind != ptproto.LineFrame || ev.Frame.Port != "sd.file" {
				continue
			}
			ff := fields(ev.Line)
			size, _ := strconv.ParseInt(ff["size"], 10, 64)
			files = append(files, File{Name: ff["name"], Size: size, Date: ff["date"], Time: ff["time"]})
		case <-timer.C:
			return files, nil
		}
	}
	return files, nil
}

// Remove deletes PORTTOOL/name.
func Remove(b *ptboard.Board, name string) error {
	_, err := run(b, "sd.rm name="+name)
	return err
}

// Eject stops any transfer and unmounts the card so it can be pulled.
func Eject(b *ptboard.Board) error {
	_, err := run(b, "sd.eject")
	return err
}

// NameOK is the board's rule for names: ASCII, 1-64 characters, no path
// characters (FatFs runs code page 850 there).
func NameOK(name string) bool {
	if name == "" || len(name) > 64 || name == "." || name == ".." {
		return false
	}
	for _, c := range name {
		if c < 0x20 || c > 0x7e || strings.ContainsRune(`/\:*?"<>|`, c) {
			return false
		}
	}
	return true
}
