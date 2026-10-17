package ptpanel

// The connection block's KNX USB interface row and the KNX window's PC end
// (decision 151 #8, PORTTOOL-FLOW.md C.3.6 "连接区", C.3.7 "上位机 KNX").

import (
	"encoding/json"
	"errors"
	"net/http"

	"PortTool/src/knxusb"
	"PortTool/src/ptlib"
	"PortTool/src/simboard"
)

// knxState is the row: what was found last, and which one was picked. Kept
// for this run only: a HID path changes when the interface is replugged.
type knxState struct {
	scanned     bool
	unsupported bool
	devices     []knxusb.DeviceInfo
	pick        string
	err         string
}

// knxScanLocked lists the interfaces again; c.mu is held.
func (c *connState) knxScanLocked() {
	d, err := knxusb.List()
	c.knx.scanned, c.knx.devices, c.knx.err = true, d, ""
	c.knx.unsupported = errors.Is(err, knxusb.ErrUnsupported)
	if err != nil && !c.knx.unsupported {
		c.knx.err = err.Error()
	}
	found := false
	for _, x := range d {
		found = found || x.Path == c.knx.pick
	}
	if !found {
		c.knx.pick = ""
	}
	if len(d) == 1 {
		c.knx.pick = d[0].Path
	}
}

func (c *connState) knxJSONLocked() map[string]any {
	devs := c.knx.devices
	if devs == nil {
		devs = []knxusb.DeviceInfo{}
	}
	labels := make([]string, len(devs))
	for i, d := range devs {
		labels[i] = d.Label()
	}
	return map[string]any{"unsupported": c.knx.unsupported, "devices": devs, "labels": labels,
		"pick": c.knx.pick, "error": c.knx.err}
}

// handleKNXUSB: GET reports the row (listing once), POST {"rescan": true}
// lists again, POST {"pick": path} picks one.
func (s *Server) handleKNXUSB(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Rescan bool    `json:"rescan"`
		Pick   *string `json:"pick"`
	}
	if r.Method == http.MethodPost {
		_ = json.NewDecoder(r.Body).Decode(&body)
	}
	c := &s.conn
	c.mu.Lock()
	defer c.mu.Unlock()
	if body.Rescan || !c.knx.scanned {
		c.knxScanLocked()
	}
	if body.Pick != nil {
		c.knx.pick = ""
		for _, d := range c.knx.devices {
			if d.Path == *body.Pick {
				c.knx.pick = d.Path
			}
		}
	}
	writeJSON(w, 200, c.knxJSONLocked())
}

// knxPeer opens the picked interface for the KNX window. The simulated board
// has no KNX bus to share, so it gets none (C.3.7 "KNX 分析窗口").
func (s *Server) knxPeer(port string) func() (ptlib.KNXPeer, error) {
	return func() (ptlib.KNXPeer, error) {
		if simboard.IsSim(port) {
			return nil, ptlib.KNXUnavailable{Why: ptlib.KNXWhySim}
		}
		c := &s.conn
		c.mu.Lock()
		if !c.knx.scanned {
			c.knxScanLocked()
		}
		st := c.knx
		c.mu.Unlock()
		switch {
		case st.unsupported:
			return nil, ptlib.KNXUnavailable{Why: ptlib.KNXWhyUnsupported}
		case len(st.devices) == 0:
			return nil, ptlib.KNXUnavailable{Why: ptlib.KNXWhyNone, Detail: st.err}
		case st.pick == "":
			return nil, ptlib.KNXUnavailable{Why: ptlib.KNXWhyPick}
		}
		var info knxusb.DeviceInfo
		for _, d := range st.devices {
			if d.Path == st.pick {
				info = d
			}
		}
		dev, err := knxusb.OpenHID(info.Path)
		if err != nil {
			return nil, ptlib.KNXUnavailable{Why: ptlib.KNXWhyOpen, Detail: err.Error()}
		}
		k, err := knxusb.Open(dev, info.Label())
		if err != nil {
			return nil, ptlib.KNXUnavailable{Why: ptlib.KNXWhyOpen, Detail: err.Error()}
		}
		return k, nil
	}
}
