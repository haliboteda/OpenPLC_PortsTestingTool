package ptreport

import (
	"bytes"
	"encoding/json"
	"testing"

	"PortTool/src/ptcheck"
)

func failed(field string, op ptcheck.Op) ptcheck.Result {
	return ptcheck.Result{Check: ptcheck.Check{Field: field, Op: op}, Pass: false}
}

func passed(field string, op ptcheck.Op) ptcheck.Result {
	return ptcheck.Result{Check: ptcheck.Check{Field: field, Op: op}, Pass: true}
}

// The spelling of PORTTOOL-FLOW.md C.3.6 「失败码」 (decision 150).
func TestFailCodeSpelling(t *testing.T) {
	for _, c := range []struct {
		where  string
		ch     int
		o      Outcome
		reason string
		checks []ptcheck.Result
		want   string
	}{
		// Not passed: the first failed criterion, field then operator.
		{"rs485.peer", 0, OutcomeFail, "", []ptcheck.Result{passed("junk", ptcheck.OpEq), failed("miss", ptcheck.OpCountZero), failed("overrun", ptcheck.OpCountZero)}, "rs485.peer.miss-count_zero"},
		// An indexed field's [n] is written _n.
		{"temp.read.T1", 0, OutcomeFail, "", []ptcheck.Result{failed("ch1[1]", ptcheck.OpRange)}, "temp.read.T1.ch1_1-range"},
		// A channel takes the failed criterion naming it before the first one.
		{"temp.read.T2", 2, OutcomeFail, "", []ptcheck.Result{failed("ch1[1]", ptcheck.OpRange), failed("ch2[1]", ptcheck.OpRange)}, "temp.read.T2.ch2_1-range"},
		{"din.level.DI3", 3, OutcomeFail, "", []ptcheck.Result{failed("v", ptcheck.OpEq)}, "din.level.DI3.v-eq"},
		// No criterion to point at: the reason code, else fail.
		{"led.blink", 0, OutcomeFail, "operator_no", nil, "led.blink.operator_no"},
		{"dout.load.DO1", 1, OutcomeFail, "", nil, "dout.load.DO1.fail"},
		// Error, stopped, timeout: the reason code, else the outcome.
		{"ain.read1", 0, OutcomeError, "not_calibrated", nil, "ain.read1.not_calibrated"},
		{"aout.load.AO2", 2, OutcomeError, "no_reading", nil, "aout.load.AO2.no_reading"},
		{"rs232.recover", 0, OutcomeStopped, "no_recovery", nil, "rs232.recover.no_recovery"},
		{"sdram.sweep", 0, OutcomeTimeout, "", nil, "sdram.sweep.timeout"},
		{"eth-link", 0, OutcomeError, "", nil, "eth-link.error"},
		// A sentence is not a code.
		{"functional", 0, OutcomeStopped, "stopped by the operator", nil, "functional.stopped"},
		{"user", 0, OutcomeError, "demo upload: exit 3", nil, "user.error"},
		// Passed, skipped and recorded carry no code.
		{"sd.probe", 0, OutcomePass, "", nil, ""},
		{"sd.probe", 0, OutcomeSkipped, "not_ticked", nil, ""},
		{"knx.recover", 0, "RECORDED", "", nil, ""},
	} {
		if got := FailCode(c.where, c.ch, c.o, c.reason, c.checks); got != c.want {
			t.Errorf("FailCode(%q, %d, %s, %q) = %q, want %q", c.where, c.ch, c.o, c.reason, got, c.want)
		}
	}
	if got := ChannelPart("DI1/DI2"); got != "DI1+DI2" {
		t.Errorf("a quadrature group's name: %q", got)
	}
}

// The report's head lists every code once, in order, and the file carries it.
func TestReportSumsUpItsFailCodes(t *testing.T) {
	r := Report{Steps: []StepResult{
		{ID: "rs485.peer", Outcome: OutcomeFail, FailCode: "rs485.peer.miss-count_zero"},
		{ID: "aout.load", Outcome: OutcomeError, Channels: []ChannelResult{
			{Ch: 1, Name: "AO1", Outcome: OutcomePass},
			{Ch: 2, Name: "AO2", Outcome: OutcomeError, FailCode: "aout.load.AO2.no_reading"}}},
		{ID: "functional", Outcome: OutcomeFail, FailCode: "rs485.peer.miss-count_zero", Items: []ItemRun{
			{No: 1, Outcome: OutcomeFail, FailCode: "rs485.peer.miss-count_zero"},
			{No: 2, Outcome: OutcomeTimeout, FailCode: "sdram.sweep.timeout"}}},
	}}
	want := []string{"rs485.peer.miss-count_zero", "aout.load.AO2.no_reading", "sdram.sweep.timeout"}
	got := r.CollectFailCodes()
	if len(got) != len(want) {
		t.Fatalf("fail_codes %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("fail_codes %v, want %v", got, want)
		}
	}
	var buf bytes.Buffer
	if err := r.WriteJSON(&buf); err != nil {
		t.Fatal(err)
	}
	var back struct {
		FailCodes []string `json:"fail_codes"`
		Steps     []struct {
			Channels []struct {
				FailCode string `json:"fail_code"`
			} `json:"channels"`
		} `json:"steps"`
	}
	if err := json.Unmarshal(buf.Bytes(), &back); err != nil {
		t.Fatal(err)
	}
	if len(back.FailCodes) != 3 || back.Steps[1].Channels[1].FailCode != "aout.load.AO2.no_reading" {
		t.Errorf("the written report lost its codes: %s", buf.String())
	}
}
