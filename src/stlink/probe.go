package stlink

import (
	"fmt"
	"strings"
)

// Probe is one ST-Link the programmer can see.
type Probe struct {
	SN    string `json:"sn"`
	FW    string `json:"fw"`
	Board string `json:"board"`
}

// ListProbes asks STM32_Programmer_CLI which ST-Links are plugged in. Read
// only: it touches no board. An empty list with a nil error means none.
// $PROD/docs/modules/M4/PORTTOOL-FLOW.md C.3.6, "连接区".
func ListProbes() ([]Probe, error) {
	cli, err := Find()
	if err != nil {
		return nil, err
	}
	out, err := Run(cli, "--list", "st")
	probes := parseProbes(out)
	// The CLI may exit non-zero when nothing is plugged in, so an error only
	// counts when the output never reached the ST-Link section.
	if err != nil && len(probes) == 0 && !strings.Contains(out, "STLink Interface") {
		return nil, fmt.Errorf("STM32_Programmer_CLI --list st: %v\n%s", err, lastLines(out, 4))
	}
	return probes, nil
}

// parseProbes reads the "STLink Interface" section only: the UART section
// repeats each probe's SN under its virtual COM port.
func parseProbes(out string) []Probe {
	var probes []Probe
	in := false
	for _, raw := range strings.Split(out, "\n") {
		line := strings.TrimSpace(raw)
		if strings.HasPrefix(line, "=====") {
			in = strings.Contains(line, "STLink Interface")
			continue
		}
		if !in {
			continue
		}
		if strings.HasPrefix(line, "ST-Link Probe") {
			probes = append(probes, Probe{})
			continue
		}
		key, val, ok := strings.Cut(line, ":")
		if !ok || len(probes) == 0 {
			continue
		}
		p := &probes[len(probes)-1]
		val = strings.TrimSpace(val)
		switch strings.TrimSpace(key) {
		case "ST-LINK SN":
			p.SN = val
		case "ST-LINK FW":
			p.FW = val
		case "Board Name":
			p.Board = val
		}
	}
	return probes
}
