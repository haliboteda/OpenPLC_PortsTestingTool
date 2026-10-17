package knxusb

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// Linux: /dev/hidrawN, described by /sys/class/hidraw/hidrawN/device.
func listHID() ([]DeviceInfo, error) {
	dirs, _ := filepath.Glob("/sys/class/hidraw/hidraw*")
	sort.Strings(dirs)
	var out []DeviceInfo
	for _, d := range dirs {
		desc, err := os.ReadFile(d + "/device/report_descriptor")
		if err != nil || !parseDescriptor(desc).knxShaped() {
			continue
		}
		info := DeviceInfo{Path: "/dev/" + filepath.Base(d)}
		ue, _ := os.ReadFile(d + "/device/uevent")
		bus := parseUevent(string(ue), &info)
		if bus != 0x03 { // USB only
			continue
		}
		out = append(out, info)
	}
	return out, nil
}

// parseUevent reads HID_ID=0003:00000E77:00000104 and HID_NAME=...; it
// returns the bus type.
func parseUevent(ue string, info *DeviceInfo) uint64 {
	var bus uint64
	for _, line := range strings.Split(ue, "\n") {
		if v, ok := strings.CutPrefix(line, "HID_ID="); ok {
			p := strings.Split(strings.TrimSpace(v), ":")
			if len(p) == 3 {
				bus, _ = strconv.ParseUint(p[0], 16, 32)
				vid, _ := strconv.ParseUint(p[1], 16, 32)
				pid, _ := strconv.ParseUint(p[2], 16, 32)
				info.VID, info.PID = uint16(vid), uint16(pid)
			}
		}
		if v, ok := strings.CutPrefix(line, "HID_NAME="); ok {
			info.Name = strings.TrimSpace(v)
		}
	}
	return bus
}

type hidraw struct{ f *os.File }

func openHID(path string) (HID, error) {
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		if errors.Is(err, os.ErrPermission) {
			return nil, fmt.Errorf("%s: no permission to open it (a udev rule giving this user read/write access is needed): %w", path, err)
		}
		return nil, err
	}
	return &hidraw{f: f}, nil
}

func (h *hidraw) ReadReport() ([]byte, error) {
	b := make([]byte, ReportLen)
	n, err := h.f.Read(b)
	if err != nil {
		return nil, err
	}
	return b[:n], nil
}

func (h *hidraw) WriteReport(r []byte) error {
	_, err := h.f.Write(r)
	return err
}

func (h *hidraw) Close() error { return h.f.Close() }
