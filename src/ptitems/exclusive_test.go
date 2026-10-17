package ptitems

import "testing"

// Same group and both once-only or both repeatable: excluded. A once-only and
// a repeatable item run one after the other (decision 128).
func TestExcludes(t *testing.T) {
	by := map[string]Item{}
	for _, it := range Catalogue {
		by[it.ID] = it
	}
	for _, c := range []struct {
		a, b string
		want bool
	}{
		{"usb.info", "usb.xfer", false},
		{"can.loop", "can.ext", true},
		{"dout.load", "dout.blink", true},
		{"usb.info", "usb.info", false},
		{"temp.read", "sd.rw", false},
	} {
		if got := Excludes(by[c.a], by[c.b]); got != c.want {
			t.Errorf("Excludes(%s, %s) = %v, want %v", c.a, c.b, got, c.want)
		}
	}
}
