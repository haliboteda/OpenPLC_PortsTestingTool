package ptpanel

import (
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"PortTool/src/ptboard"
	"PortTool/src/ptecho"
	"PortTool/src/ptsdfile"
	"PortTool/src/serialx"
	"PortTool/src/simboard"
)

// The SD card's file transfer block on the R&D SD port: write a file from
// this machine, list, view, save, delete, eject. PORTTOOL-FLOW.md B.11 and
// C.3.6 「SD 文件传递」, DECISIONS.md 120.

// sdfMaxUpload bounds one upload: the whole file is held in memory here.
const sdfMaxUpload = 64 << 20

// sdfPreview is how much of a file the 查看 view shows.
const sdfPreview = 4096

var sdfMu sync.Mutex // one transfer at a time; the board has one slot

func (s *Server) sdfileRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/api/sd/put", s.handleSDPut)
	mux.HandleFunc("/api/sd/ls", s.handleSDList)
	mux.HandleFunc("/api/sd/get", s.handleSDGet)
	mux.HandleFunc("/api/sd/rm", s.handleSDRemove)
	mux.HandleFunc("/api/sd/eject", s.handleSDEject)
}

// sdfBoard is the connected board when nothing else is using it.
func (s *Server) sdfBoard() (*ptboard.Board, msg, bool) {
	s.mu.Lock()
	b, timed := s.board, s.run != nil
	s.mu.Unlock()
	if b == nil {
		return nil, m("go.sdf.not_connected"), false
	}
	s.full.mu.Lock()
	full := s.full.running
	s.full.mu.Unlock()
	if timed || full || s.libBusy() {
		return nil, m("go.sdf.busy"), false
	}
	s.waitIPProbe()
	return b, msg{}, true
}

// sdfOpener is the data channel for via. A peer bound to that link in the
// connection zone holds the board's one client slot, so it is let go first.
func (s *Server) sdfOpener(via string) (ptsdfile.Opener, msg, bool) {
	s.mu.Lock()
	sim, l := simboard.IsSim(s.portNam), s.links[via]
	addr, caps := s.ip.addr, s.caps
	s.mu.Unlock()
	switch via {
	case "eth":
		if addr == "" {
			return nil, m("go.sdf.no_ip"), false
		}
		port := "5000"
		if p, ok := caps.Port("eth"); ok && p.Values["port"] != "" {
			port = p.Values["port"]
		}
		if l != nil {
			s.unbindLink(via)
			s.say(m("go.sdf.unbound", "port", via))
		}
		target := net.JoinHostPort(addr, port)
		return func() (io.ReadWriteCloser, error) {
			var err error
			for end := time.Now().Add(3 * time.Second); time.Now().Before(end); time.Sleep(100 * time.Millisecond) {
				var c net.Conn
				if c, err = net.DialTimeout("tcp", target, time.Second); err == nil {
					return c, nil
				}
			}
			return nil, err
		}, msg{}, true
	case "usb":
		if sim {
			return nil, m("go.sdf.sim_usb"), false
		}
		com := ""
		if l != nil {
			com = l.COM
			s.unbindLink(via)
			s.say(m("go.sdf.unbound", "port", via))
		} else {
			c, err := ptecho.FindCDC()
			if err != nil {
				return nil, m("go.sdf.no_cdc", "detail", err), false
			}
			com = c
		}
		return func() (io.ReadWriteCloser, error) { return s.Open(com, serialx.DefaultBaud) }, msg{}, true
	}
	return nil, m("go.sdf.bad_via"), false
}

// sdfErr words a failed transfer: the board's error= code, or the cause.
func sdfErr(err error) msg {
	var e *ptsdfile.Error
	if errors.As(err, &e) {
		key, ok := sdfErrKey[e.Code]
		if !ok {
			key = "go.sdf.err.other"
		}
		return m(key, "detail", e.Detail)
	}
	return m("go.sdf.failed", "detail", err)
}

// sdfErrKey is the sentence for each error= code porttool_sdfile.c reports.
var sdfErrKey = map[string]string{
	"name":           "go.sdf.err.name",
	"size":           "go.sdf.err.size",
	"nocard":         "go.sdf.err.nocard",
	"open":           "go.sdf.err.open",
	"write":          "go.sdf.err.write",
	"readback":       "go.sdf.err.readback",
	"timeout":        "go.sdf.err.timeout",
	"overrun":        "go.sdf.err.overrun",
	"too_many_bytes": "go.sdf.err.too_many_bytes",
	"busy":           "go.sdf.err.busy",
	"list":           "go.sdf.err.list",
	"remove":         "go.sdf.err.remove",
	"read":           "go.sdf.err.read",
	"stopped":        "go.sdf.err.stopped",
	"replaced":       "go.sdf.err.replaced",
	"ejected":        "go.sdf.err.ejected",
}

func (s *Server) handleSDPut(w http.ResponseWriter, r *http.Request) {
	via, name := r.URL.Query().Get("via"), r.URL.Query().Get("name")
	if !ptsdfile.NameOK(name) {
		writeErr(w, 400, m("go.sdf.err.name"))
		return
	}
	data, err := io.ReadAll(io.LimitReader(r.Body, sdfMaxUpload+1))
	if err != nil || len(data) > sdfMaxUpload {
		writeErr(w, 400, m("go.sdf.too_big", "mb", sdfMaxUpload>>20))
		return
	}
	if !sdfMu.TryLock() {
		writeErr(w, 409, m("go.sdf.busy"))
		return
	}
	defer sdfMu.Unlock()
	b, why, ok := s.sdfBoard()
	if !ok {
		writeErr(w, 409, why)
		return
	}
	open, why, ok := s.sdfOpener(via)
	if !ok {
		writeErr(w, 409, why)
		return
	}
	res, err := ptsdfile.Put(b, via, open, name, data, nil)
	if err != nil {
		writeErr(w, 200, sdfErr(err))
		return
	}
	kbps := 0.0
	if sec := res.Elapsed.Seconds(); sec > 0 {
		kbps = float64(res.Bytes) / 1024 / sec
	}
	s.say(m("go.sdf.put_done", "name", name, "bytes", res.Bytes, "ok", res.OK()))
	writeJSON(w, 200, map[string]any{"ok": res.OK(), "bytes": res.Bytes, "wrote": res.Wrote,
		"want_crc": fmt.Sprintf("%08X", res.WantCRC), "got_crc": fmt.Sprintf("%08X", res.GotCRC),
		"kbps": kbps, "via": via})
}

func (s *Server) handleSDList(w http.ResponseWriter, r *http.Request) {
	if !sdfMu.TryLock() {
		writeErr(w, 409, m("go.sdf.busy"))
		return
	}
	defer sdfMu.Unlock()
	b, why, ok := s.sdfBoard()
	if !ok {
		writeErr(w, 409, why)
		return
	}
	files, err := ptsdfile.List(b)
	if err != nil {
		writeErr(w, 200, sdfErr(err))
		return
	}
	writeJSON(w, 200, map[string]any{"files": files})
}

func (s *Server) handleSDGet(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	via, name := q.Get("via"), q.Get("name")
	if !ptsdfile.NameOK(name) {
		writeErr(w, 400, m("go.sdf.err.name"))
		return
	}
	if !sdfMu.TryLock() {
		writeErr(w, 409, m("go.sdf.busy"))
		return
	}
	defer sdfMu.Unlock()
	b, why, ok := s.sdfBoard()
	if !ok {
		writeErr(w, 409, why)
		return
	}
	open, why, ok := s.sdfOpener(via)
	if !ok {
		writeErr(w, 409, why)
		return
	}
	data, res, err := ptsdfile.Get(b, via, open, name)
	if err != nil {
		writeErr(w, 200, sdfErr(err))
		return
	}
	if q.Get("download") == "1" {
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
		w.Header().Set("Content-Length", strconv.Itoa(len(data)))
		_, _ = w.Write(data)
		return
	}
	out := map[string]any{"ok": res.OK(), "size": res.Size,
		"board_crc": fmt.Sprintf("%08X", res.BoardCRC), "got_crc": fmt.Sprintf("%08X", res.GotCRC)}
	head := data[:min(len(data), sdfPreview)]
	if sdfIsText(head) {
		out["text"] = string(head)
	} else {
		out["hex"] = sdfHex(head)
	}
	out["truncated"] = len(data) > sdfPreview
	writeJSON(w, 200, out)
}

// sdfIsText is valid UTF-8 with no control bytes but tab and line ends; a
// cut at sdfPreview may split the last rune, which is allowed.
func sdfIsText(b []byte) bool {
	for len(b) > 0 {
		r, n := utf8.DecodeRune(b)
		if r == utf8.RuneError && n <= 1 {
			return len(b) < utf8.UTFMax
		}
		if r < 0x20 && r != '\t' && r != '\n' && r != '\r' {
			return false
		}
		b = b[n:]
	}
	return true
}

// sdfHex is a 16-bytes-a-line dump with the offset in front.
func sdfHex(b []byte) string {
	var sb strings.Builder
	for off := 0; off < len(b); off += 16 {
		end := min(off+16, len(b))
		fmt.Fprintf(&sb, "%06X ", off)
		for _, c := range b[off:end] {
			fmt.Fprintf(&sb, " %02X", c)
		}
		sb.WriteByte('\n')
	}
	return sb.String()
}

func (s *Server) handleSDRemove(w http.ResponseWriter, r *http.Request) {
	name := r.URL.Query().Get("name")
	if !ptsdfile.NameOK(name) {
		writeErr(w, 400, m("go.sdf.err.name"))
		return
	}
	if !sdfMu.TryLock() {
		writeErr(w, 409, m("go.sdf.busy"))
		return
	}
	defer sdfMu.Unlock()
	b, why, ok := s.sdfBoard()
	if !ok {
		writeErr(w, 409, why)
		return
	}
	if err := ptsdfile.Remove(b, name); err != nil {
		writeErr(w, 200, sdfErr(err))
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}

func (s *Server) handleSDEject(w http.ResponseWriter, r *http.Request) {
	if !sdfMu.TryLock() {
		writeErr(w, 409, m("go.sdf.busy"))
		return
	}
	defer sdfMu.Unlock()
	b, why, ok := s.sdfBoard()
	if !ok {
		writeErr(w, 409, why)
		return
	}
	if err := ptsdfile.Eject(b); err != nil {
		writeErr(w, 200, sdfErr(err))
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}
