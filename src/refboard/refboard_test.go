package refboard

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func keys(ps []Problem) []string {
	var out []string
	for _, p := range ps {
		out = append(out, p.Key)
	}
	return out
}

// The default is one to one, DO1-6 through RY1-6, and passes its own check (decision 133).
func TestDefaultWiring(t *testing.T) {
	w := Default()
	if p := w.Check(); len(p) != 0 {
		t.Fatalf("the default is refused: %v", p)
	}
	if l := w.DOLink(3); l.Via != 3 || l.To != 3 {
		t.Errorf("DO3: %+v, want through RY3 to DI3", l)
	}
	if l := w.DOLink(8); l.Via != 0 || l.To != 8 {
		t.Errorf("DO8: %+v, want direct to DI8", l)
	}
	if w.RefFor("AI2") != "AO2" || w.RefFor("AI1") != "AO1" || w.Analog["AI1"] != "AO1" || w.Analog["AI2"] != "AO2" || w.DOOfRelay(6) != 6 {
		t.Errorf("analog / relay defaults: AI2<-%q AI1<-%q RY6 on DO%d", w.RefFor("AI2"), w.RefFor("AI1"), w.DOOfRelay(6))
	}
}

// Decision 135: a channel wired twice or a relay in two paths is refused, and
// nothing is written.
func TestCheckRefusesWhatCannotBeWired(t *testing.T) {
	cases := []struct {
		name string
		edit func(w *Wiring)
		want string
	}{
		{"reference DI twice", func(w *Wiring) { w.DO[2] = Link{Via: 2, To: 1} }, "refboard.ref_twice"},
		{"relay twice", func(w *Wiring) { w.DO[7] = Link{Via: 1, To: 7} }, "refboard.relay_twice"},
		{"DUT AI twice", func(w *Wiring) { w.Analog["AO2"] = "AI1" }, "refboard.dut_twice"},
		{"AO to AO", func(w *Wiring) { w.Analog["AO2"] = "AO1" }, "refboard.bad_pair"},
		{"DI period under the floor", func(w *Wiring) { w.Params.DIPeriod = 20 }, "refboard.bad_param"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w := Default()
			c.edit(&w)
			got := keys(w.Check())
			if len(got) != 1 || got[0] != c.want {
				t.Fatalf("problems %v, want %s", got, c.want)
			}
			dir := t.TempDir()
			if _, ps, err := Save(dir, "UID", w, time.Now()); err != nil || len(ps) == 0 {
				t.Fatalf("saved anyway: %v %v", ps, err)
			}
			if _, err := os.Stat(filepath.Join(dir, fileName)); !os.IsNotExist(err) {
				t.Fatal("a refused registration was written")
			}
		})
	}
}

func TestSaveAndLoad(t *testing.T) {
	dir := t.TempDir()
	if _, saved, _ := Load(dir); saved {
		t.Fatal("nothing saved yet")
	}
	w := Default()
	w.DO[1] = Link{Via: 0, To: 1}
	w.Analog["AO1"], w.Analog["AO2"] = "AI2", "AI1"
	w.Params.DOHold = 300
	if _, ps, err := Save(dir, "UID", w, time.Now()); err != nil || len(ps) != 0 {
		t.Fatalf("save: %v %v", ps, err)
	}
	got, saved, err := Load(dir)
	if err != nil || !saved || got.DOLink(1).Via != 0 || got.RefFor("AI1") != "AO2" || got.Params.DOHold != 300 {
		t.Fatalf("loaded %+v saved=%v err=%v", got, saved, err)
	}
}
