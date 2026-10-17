package ptpanel

import (
	"net/http/httptest"
	"testing"
	"time"

	"PortTool/src/ptboard"
	"PortTool/src/ptproto"
)

// The 网口 row learns the board's address right after connecting
// (PORTTOOL-FLOW.md C.3.6 「连接区」).

func ethFrame(t *testing.T, body string) ptboard.Event {
	t.Helper()
	f, ok := ptproto.ParseFrame(body)
	if !ok {
		t.Fatalf("not a frame: %s", body)
	}
	return ptboard.Event{Kind: ptproto.LineFrame, Frame: f}
}

func TestFrameIPIgnoresAnAddressNotYetGiven(t *testing.T) {
	for body, want := range map[string]string{
		"eth t=1 seq=1 rx=0 miss=0 ip=0.0.0.0 link=1":      "",
		"eth t=1 seq=1 rx=0 miss=0 ip=192.168.1.50 link=1": "192.168.1.50",
		"can t=1 seq=1 rx=0 miss=0 ip=192.168.1.50":        "",
	} {
		if got := frameIP(ethFrame(t, body)); got != want {
			t.Errorf("%s: got %q, want %q", body, got, want)
		}
	}
}

func TestBoardAddressFoundAfterConnectingWithoutATest(t *testing.T) {
	needSim(t)
	s := New()
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()
	defer s.Close()

	if st := call(t, srv, "/api/connect", map[string]any{"port": "sim"}); st["connected"] != true {
		t.Fatalf("connect: %v", st)
	}
	deadline := time.Now().Add(ipProbeTimeout + 5*time.Second)
	for {
		ip, _ := call(t, srv, "/api/conn", nil)["boardIP"].(map[string]any)
		if ip["state"] == "ok" {
			if ip["addr"] != "127.0.0.1" || ip["sim"] != true {
				t.Fatalf("simulated board's address: %v", ip)
			}
			break
		}
		if ip["state"] == "none" || time.Now().After(deadline) {
			t.Fatalf("no address after connecting: %v", ip)
		}
		time.Sleep(200 * time.Millisecond)
	}
	// The probe's session is stopped, so a test can start eth straight away.
	if st := call(t, srv, "/api/command", map[string]any{"cmd": "pt.start eth"}); st["error"] != nil && st["error"] != "" {
		t.Fatalf("eth still held by the probe: %v", st)
	}
	call(t, srv, "/api/command", map[string]any{"cmd": "pt.stop eth"})
}
