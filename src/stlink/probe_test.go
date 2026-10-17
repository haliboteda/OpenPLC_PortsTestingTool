package stlink_test

import (
	"errors"
	"testing"

	"PortTool/src/stlink"
)

// Captured from STM32CubeProgrammer v2.22.0 with one STLINK-V3MINIE plugged
// in, trimmed. The UART section repeats the SN and must not count twice.
const oneProbe = `
=====  DFU Interface   =====
No STM32 device in DFU mode connected
===== STLink Interface =====

-------- Connected ST-LINK Probes List --------

ST-Link Probe 0 :
   ST-LINK SN  : 002A001D3033510735393935
   ST-LINK FW  : V3J17M11
   Access Port Number  : 3
   Board Name  : STLINK-V3MINIE
-----------------------------------------------

=====  UART Interface  =====
Board Name  : STLINK-V3MINIE
ST-LINK SN: 002A001D3033510735393935
Port: COM4
`

// No probe: the section is there and lists nothing. Wording not captured from
// a real run - the parser only looks for probe entries.
const noProbe = `
===== STLink Interface =====
No ST-LINK detected!
=====  UART Interface  =====
Total number of serial ports available: 0
`

// answer puts the fake CLI in place and makes it answer --list with out.
func answer(t *testing.T, out string, err error) {
	t.Helper()
	fake(t)
	inner := stlink.Run
	stlink.Run = func(cli string, args ...string) (string, error) {
		if len(args) > 0 && args[0] == "--list" {
			return out, err
		}
		return inner(cli, args...)
	}
}

func TestListProbesNoneConnected(t *testing.T) {
	answer(t, noProbe, errors.New("exit status 1"))
	got, err := stlink.ListProbes()
	if err != nil || len(got) != 0 {
		t.Fatalf("want no probe and no error, got %v %v", got, err)
	}
}

func TestListProbesReadsOneProbeOnce(t *testing.T) {
	answer(t, oneProbe, nil)
	got, err := stlink.ListProbes()
	if err != nil || len(got) != 1 {
		t.Fatalf("want one probe, got %v %v", got, err)
	}
	if got[0].SN != "002A001D3033510735393935" || got[0].FW != "V3J17M11" || got[0].Board != "STLINK-V3MINIE" {
		t.Fatalf("fields: %+v", got[0])
	}
}

func TestListProbesReportsACLIThatNeverListed(t *testing.T) {
	answer(t, "Error: something else", errors.New("exit status 1"))
	if _, err := stlink.ListProbes(); err == nil {
		t.Fatal("a CLI failure before the ST-Link section must be an error, not 'none'")
	}
}
