package ptpanel

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"PortTool/src/calarea"
	"PortTool/src/calstore"
	"PortTool/src/ptboard"
	"PortTool/src/ptproto"
)

// handleCalBurn writes the connected board's archived calibration image into
// sector 15 over the control port: pt.calw, the fixture firmware erases and
// writes the sector itself (decision 142). The board is asked for its UID now,
// so a swapped board never gets another board's values; pt.cal afterwards
// must give back the image's k, b. PORTTOOL-FLOW.md C.3.2.
func (s *Server) handleCalBurn(w http.ResponseWriter, r *http.Request) {
	uid, err := s.boardUID()
	if err != nil {
		writeJSON(w, 200, map[string]any{"error": msgOf(err)})
		return
	}
	img := calstore.ImagePath(uid)
	data, err := os.ReadFile(img)
	if errors.Is(err, os.ErrNotExist) {
		writeJSON(w, 200, map[string]any{"error": m("go.calburn.no_image", "dir", filepath.Dir(img))})
		return
	}
	if err != nil {
		writeJSON(w, 200, map[string]any{"error": m("go.detail", "detail", err.Error())})
		return
	}
	cmd, err := ptproto.CalWriteCommand(data)
	if err != nil {
		writeJSON(w, 200, map[string]any{"error": m("go.calburn.bad_image", "image", img, "detail", err.Error())})
		return
	}
	_, want, _ := calarea.Decode(data)

	s.mu.Lock()
	b := s.board
	s.mu.Unlock()
	if b == nil {
		writeJSON(w, 200, map[string]any{"error": m("go.uid.no_board")})
		return
	}

	fail := func(x msg) {
		s.say(x)
		writeJSON(w, 200, map[string]any{"error": x})
	}
	s.say(m("go.calburn.start", "image", img))
	if _, err := b.Send(cmd, ptboard.ExpectFor(cmd), ptboard.TimeoutFor(cmd)); err != nil {
		fail(m("go.calburn.failed", "detail", err.Error()))
		return
	}
	cal := s.readBoardCal(uid)
	if cal.State != calOK {
		fail(m("go.calburn.readback_bad"))
		return
	}
	var kbs []string
	for i, n := range calarea.Names {
		got := cal.Ch[n]
		if float32(got.K) != want[i].Gain || float32(got.B) != want[i].Offset {
			fail(m("go.calburn.readback_diff", "ch", n))
			return
		}
		kbs = append(kbs, fmt.Sprintf("%s k=%.5g b=%.5g", n, got.K, got.B))
	}
	done := m("go.calburn.done", "uid", uid, "kb", strings.Join(kbs, ", "))
	s.say(done)
	writeJSON(w, 200, map[string]any{"ok": true, "image": img, "board": cal, "done": done})
}
