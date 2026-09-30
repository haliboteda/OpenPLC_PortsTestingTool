package simboard

import (
	"os"
	"path/filepath"
	"testing"
)

// plant builds the harness layout under root and returns the path it put the
// binary at.
func plant(t *testing.T, root string) string {
	t.Helper()
	dir := filepath.Join(root, "TestCase", "host", "porttool_caps", "harness")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, exeName())
	if err := os.WriteFile(p, []byte("not really a binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestSearchUpFindsItFromBelow(t *testing.T) {
	root := t.TempDir()
	want := plant(t, root)

	// Output/windows is where the panel actually lives relative to the
	// harness, so this is the real shape and not an invented one.
	from := filepath.Join(root, "Output", "windows")
	if err := os.MkdirAll(from, 0o755); err != nil {
		t.Fatal(err)
	}

	got, ok := searchUp(from)
	if !ok {
		t.Fatal("did not find the harness two levels up")
	}
	if got != want {
		t.Errorf("found %q, want %q", got, want)
	}
}

// *** The case the 2026-09-14 fix is about. *** A directory with no repository
// above it has to come back empty rather than matching something by accident -
// this is what a Start-menu shortcut's working directory looks like.
func TestSearchUpGivesUpWhereThereIsNothing(t *testing.T) {
	if _, ok := searchUp(t.TempDir()); ok {
		t.Error("found a harness under an empty directory")
	}
}

// The walk is bounded, so a deep tree cannot make it climb to the filesystem
// root looking for something that is not there.
func TestSearchUpStopsClimbing(t *testing.T) {
	root := t.TempDir()
	plant(t, root)

	deep := root
	for i := 0; i < 12; i++ {
		deep = filepath.Join(deep, "d")
	}
	if err := os.MkdirAll(deep, 0o755); err != nil {
		t.Fatal(err)
	}

	if _, ok := searchUp(deep); ok {
		t.Error("climbed more than the bound allows")
	}
}

func TestPortToolSimWins(t *testing.T) {
	root := t.TempDir()
	planted := plant(t, root)

	elsewhere := filepath.Join(t.TempDir(), "chosen"+filepath.Ext(exeName()))
	if err := os.WriteFile(elsewhere, []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}

	t.Setenv("PORTTOOL_SIM", elsewhere)
	got, err := Find()
	if err != nil {
		t.Fatal(err)
	}
	if got != elsewhere {
		t.Errorf("Find returned %q, want the override %q", got, elsewhere)
	}
	if got == planted {
		t.Error("the override lost to the search")
	}
}

// An override pointing at nothing is an error rather than a quiet fall back to
// the search: somebody who set the variable meant that binary, and silently
// running a different one is the kind of help nobody wants.
func TestPortToolSimPointingNowhereIsAnError(t *testing.T) {
	t.Setenv("PORTTOOL_SIM", filepath.Join(t.TempDir(), "absent.exe"))
	if _, err := Find(); err == nil {
		t.Error("a PORTTOOL_SIM that does not exist should not be ignored")
	}
}

func TestIsSim(t *testing.T) {
	for _, name := range []string{"sim", "SIM", " Sim "} {
		if !IsSim(name) {
			t.Errorf("IsSim(%q) = false", name)
		}
	}
	// A real port must never be taken for the simulator, however it is spelled.
	for _, name := range []string{"COM5", "simulator", "/dev/ttyUSB0", ""} {
		if IsSim(name) {
			t.Errorf("IsSim(%q) = true", name)
		}
	}
}
