// Package ptlib runs the test items a person ticked: catalogue item -> plan
// steps -> executor, once or for a duration.
// $PROD/docs/modules/M4/PORTTOOL-FLOW.md C.3.6 "测试项怎么跑"; STG-26 for the
// timed part.
//
// Nothing here judges: criteria are the plan's steps and ptcheck evaluates
// them (decision 77).
package ptlib

import (
	"fmt"
	"strconv"
	"strings"

	"PortTool/src/ptitems"
	"PortTool/src/ptplan"
	"PortTool/src/ptproto"
	"PortTool/src/ptreport"
)

// Recorded is an item that ran but has no criteria: its readings are kept,
// nothing is judged.
const Recorded ptreport.Outcome = "RECORDED"

// Pick is one item as a person set it on its card.
type Pick struct {
	ID     string            `json:"id"`
	On     bool              `json:"on"`
	Params map[string]string `json:"params,omitempty"`
	Peer   ptitems.Peer      `json:"peer,omitempty"`
	// Points are an AI / AO read or output item's targets in the range's
	// unit; empty takes the defaults (analog.go).
	Points []float64 `json:"points,omitempty"`
}

// Why an item is not run, or could not be.
const (
	ReasonNotTicked   = "not_ticked"
	ReasonCalibration = "calibration"   // runs on its calibration card (C.3.2)
	ReasonNoRefStep   = "no_ref_step"   // peer is the reference board, plan has no on:ref step for it
	ReasonNoCriteria  = "criteria_gone" // the plan lacks a step the catalogue names
	ReasonNoReading   = "no_reading"    // a reading the criteria need was left out
	ReasonAfterFail   = "after_fail"
	ReasonStopped     = "stopped"
	ReasonOperatorNo  = "operator_no"
	ReasonHoldEnded   = "hold_ended"
	ReasonNoFrames    = "no_frames"
	ReasonNoRef       = "no_ref" // the item needs the reference board and none is connected
	// ReasonBadPeer: R&D was asked for the reference board, which only the
	// fixture tests use (decision 133).
	ReasonBadPeer = "bad_peer"
)

// PickError is a set of picks the run refuses before touching the board.
type PickError struct {
	Code  string // unknown_item | exclusive | param_fixed | param_refused | points | line_too_long | bad_peer | no_target
	Item  string
	Other string // the other item of an exclusive pair
	Param string
	Why   string // the firmware's limit, in English
}

func (e *PickError) Error() string {
	return fmt.Sprintf("%s: item %s %s%s %s", e.Code, e.Item, e.Other, e.Param, e.Why)
}

// askFirst are items whose person acts before the board is read: the reset
// cause is only worth reading after somebody pressed the key (STG-03).
var askFirst = map[string]bool{"reset.cause": true}

// askWhileOn are record-only items whose person judges the output while the
// session still drives it: the PWM on a scope (STG-33).
var askWhileOn = map[string]bool{"dout.square": true}

// blinkWhileAsk are items whose lamp the PC keeps blinking while the person is
// asked: the run target's six pulses are over before the question appears
// (PORTTOOL-FLOW.md C.3.6 「指示灯」).
var blinkWhileAsk = map[string]bool{"led.blink": true}

// RecordOnly says an item's result never counts toward its port's verdict:
// KNX diagnostics without criteria (decision 103, STG-20) and recover items
// whose plan has no criteria for them (ForPlan, decision 150).
func RecordOnly(it ptitems.Item) bool {
	return (it.Port == "knx" || it.Recover) && len(it.Criteria) == 0
}

// ForPlan is items as plan judges them: a recover item whose criteria step the
// plan lacks loses its criteria and says it only records (decision 150). The
// other items keep theirs, a missing step being an error for them.
func ForPlan(items []ptitems.Item, plan ptplan.Plan) []ptitems.Item {
	out := make([]ptitems.Item, len(items))
	for i, it := range items {
		if it.Recover && len(criteriaSteps(it, plan)) == 0 {
			it.Criteria, it.Pending = nil, ptitems.RecoverPending
		}
		out[i] = it
	}
	return out
}

// IsCalibration says an item is a calibration walk, which has its own card.
func IsCalibration(it ptitems.Item) bool { return strings.Contains(it.ID, ".cal") }

// job is one item ready to run.
type job struct {
	item      ptitems.Item
	peer      ptitems.Peer
	params    map[string]string
	points    []float64
	analog    bool          // an AI / AO walk (analog.go)
	live      bool          // a person stands in for the reference board (live.go)
	drop      bool          // a person measures each DO while it is on (drop.go)
	dinWalk   bool          // a person applies 24 V input by input, the board judges (dinwalk.go)
	relay     bool          // relays switched to their counts (relay.go)
	chain     bool          // DOs read through the relays by the reference DI (chain.go)
	relayRan  []RelayChan   // the relays as runRelay left them, for relayVerdict
	load      bool          // AO under load, read per channel (aoload.go)
	recover   bool          // pulled and plugged back by a person (recover.go)
	loadSteps []ptplan.Step // the load's criteria, AO1 then AO2
	steps     []ptplan.Step // judged steps, ref companions included; nil when record-only
	record    bool
	askYesNo  bool
	askNum    bool // asked by ptlib, not by a UserReading step
	askFirst  bool
	askOn     bool             // askYesNo asked inside recordSession, answer kept below
	onAnswer  ptreport.Outcome // what the person said while the session ran
	onReason  string
	skip      string // not run: reason code
	fail      string // run refused before starting: reason code
	detail    string
}

func (j *job) port() string { return j.item.Port }

// Defaults is what a card's editable parameters start at: the criteria step's
// value, else the board's current one.
func Defaults(it ptitems.Item, plan ptplan.Plan, caps ptproto.Caps) map[string]string {
	out := map[string]string{}
	p, _ := caps.Port(it.Port)
	for _, k := range it.Params {
		if v, ok := p.Values[k]; ok {
			out[k] = v
		}
		for _, t := range it.Targets {
			if v, ok := t.Set[k]; ok {
				out[k] = v
			}
		}
		for _, st := range criteriaSteps(it, plan) {
			if v, ok := st.Params[k]; ok && !st.OnRef() {
				out[k] = v
			}
		}
	}
	return out
}

// LoadTargetOK says whether an AO load target can be in any AO range: 0 up to
// the widest full scale, 10000 mV; the run checks it against the registered range.
func LoadTargetOK(v string) bool {
	f, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
	return err == nil && f >= 0 && f <= 10000
}

// hostParamRule is what HostParamOK wants, in the words a refusal shows.
var hostParamRule = map[string]string{
	ptitems.ParamOut1:    "must be a number from 0 to AO1's range full scale",
	ptitems.ParamOut2:    "must be a number from 0 to AO2's range full scale",
	ptitems.ParamOrder:   "must be par or ser",
	ptitems.ParamBy:      "must be gap or time",
	ptitems.ParamMinutes: "must be a number above 0, or relay:number pairs",
}

// HostParamOK checks a parameter kept on the PC; true for any other.
func HostParamOK(k, v string) bool {
	if ptitems.LoadChannel(k) != "" {
		return LoadTargetOK(v)
	}
	return RelayHostParamOK(k, v)
}

// boardParams is a pick's parameters without the ones kept on the PC.
func boardParams(params map[string]string) map[string]string {
	out := map[string]string{}
	for k, v := range params {
		if !ptitems.HostParam(k) {
			out[k] = v
		}
	}
	return out
}

func criteriaSteps(it ptitems.Item, plan ptplan.Plan) []ptplan.Step {
	var out []ptplan.Step
	for _, id := range it.Criteria {
		for _, st := range plan.Steps {
			if st.ID == id {
				out = append(out, st)
			}
		}
	}
	return out
}

// build turns the picks into jobs, one per catalogue item in scope, in
// catalogue order. Unticked items become skipped jobs.
func build(items []ptitems.Item, plan ptplan.Plan, caps ptproto.Caps, picks []Pick) ([]*job, error) {
	byID := map[string]ptitems.Item{}
	for _, it := range items {
		byID[it.ID] = it
	}
	picked := map[string]Pick{}
	for _, p := range picks {
		it, ok := byID[p.ID]
		if !ok {
			return nil, &PickError{Code: "unknown_item", Item: p.ID}
		}
		if len(p.Params) > 0 {
			kept := map[string]string{}
			for k, v := range p.Params {
				kept[k] = v
			}
			ptitems.DropRetired(p.ID, kept)
			p.Params = kept
		}
		if p.On {
			if err := checkParams(it, caps, p.Params); err != nil {
				return nil, err
			}
			if len(p.Points) > 0 && (!IsAnalogReading(it) || !PointsOK(p.Points)) {
				return nil, &PickError{Code: "points", Item: p.ID}
			}
			// The AO load target has no default (decisions 129, 134).
			if TargetMissing(it, p.Params) {
				return nil, &PickError{Code: "no_target", Item: p.ID}
			}
		}
		picked[p.ID] = p
	}
	// One session cannot run in two modes at once (decision 102); once-only
	// and repeatable items of one group run one after the other (decision 128).
	var on []ptitems.Item
	for _, it := range items {
		if p, ok := picked[it.ID]; ok && p.On {
			for _, o := range on {
				if ptitems.Excludes(it, o) {
					return nil, &PickError{Code: "exclusive", Item: it.ID, Other: o.ID}
				}
			}
			on = append(on, it)
		}
	}

	var jobs []*job
	for _, it := range items {
		p, ok := picked[it.ID]
		j := &job{item: it, params: p.Params, points: p.Points}
		jobs = append(jobs, j)
		switch {
		case !ok || !p.On:
			j.skip = ReasonNotTicked
			continue
		case IsCalibration(it):
			j.skip = ReasonCalibration
			continue
		}
		j.peer = p.Peer
		if j.peer == "" && len(it.Peers) > 0 {
			j.peer = it.Peers[0]
		}
		// A live item with no criteria is live whatever its peer: the CAN
		// analyser window's peer is this PC (decision 111). A person's DI walk
		// is R&D's (decisions 128, 133).
		walk := dinWalks[it.ID] && j.peer == ptitems.PeerHuman
		if it.Live && !walk && (j.peer == ptitems.PeerHuman || len(it.Criteria) == 0) {
			j.live, j.record = true, true
			continue
		}
		if it.Recover {
			// Only the checks are taken: the walk is recover.go's, not the step's.
			j.recover, j.steps = true, criteriaSteps(it, plan)
			j.record = len(j.steps) == 0
			continue
		}
		if IsAnalogReading(it) {
			fillAnalog(j, plan)
			continue
		}
		if it.Load {
			fillLoad(j, plan)
			continue
		}
		// Relays are judged relay by relay by relayVerdict, not by plan steps.
		if it.ID == relayItem {
			j.relay = true
			continue
		}
		if onChain(it, j.peer) {
			j.chain = true
			continue
		}
		fill(j, plan)
		j.steps = offered(j.steps, caps)
		if dropWalk[it.ID] && j.peer == ptitems.PeerHuman && j.fail == "" {
			fillDrop(j)
		}
		j.dinWalk = walk && j.fail == ""
	}
	return jobs, nil
}

func checkParams(it ptitems.Item, caps ptproto.Caps, params map[string]string) error {
	if err := relayCheck(it, caps, params); err != nil {
		return err
	}
	port, _ := caps.Port(it.Port)
	for k, v := range params {
		allowed := false
		for _, n := range it.Params {
			allowed = allowed || n == k
		}
		if !allowed {
			return &PickError{Code: "param_fixed", Item: it.ID, Param: k}
		}
		if ptitems.HostParam(k) {
			if !HostParamOK(k, v) {
				return &PickError{Code: "param_refused", Item: it.ID, Param: k, Why: hostParamRule[k]}
			}
			continue
		}
		if lim, ok := port.Limits[k]; ok {
			if okv, why := lim.Accepts(v); !okv {
				return &PickError{Code: "param_refused", Item: it.ID, Param: k, Why: why + " (" + lim.Spec + ")"}
			}
		}
	}
	return nil
}

// forPeer says whether a criteria step belongs to the picked peer: a person's
// step only with a person, a PC-peer step only with the PC, and a step with a
// reference-board companion not with the PC (PORTTOOL-FLOW.md C.3.6 "一项跑哪几步").
func forPeer(st ptplan.Step, companion bool, peer ptitems.Peer) bool {
	switch {
	case st.Type == ptplan.TypeUserReading || st.Type == ptplan.TypeUserConfirm:
		return peer == ptitems.PeerHuman
	case st.Peer != nil:
		return peer == ptitems.PeerPC
	case companion:
		return peer != ptitems.PeerPC
	}
	return true
}

// CriteriaPeers is, per criteria step of the item, which of its peers run that
// step in the full test, worked out by build itself.
func CriteriaPeers(it ptitems.Item, plan ptplan.Plan) map[string][]ptitems.Peer {
	peers := append([]ptitems.Peer{}, it.Peers...)
	out := map[string][]ptitems.Peer{}
	for _, id := range it.Criteria {
		out[id] = []ptitems.Peer{}
	}
	// Which steps a peer runs does not depend on the AO load targets, which
	// have no default (decisions 129, 134); placeholders keep build from refusing.
	var params map[string]string
	if it.Load {
		params = map[string]string{ptitems.ParamOut1: "1", ptitems.ParamOut2: "1"}
	}
	for _, p := range peers {
		jobs, err := build([]ptitems.Item{it}, plan, ptproto.Caps{}, []Pick{{ID: it.ID, On: true, Peer: p, Params: params}})
		if err != nil || len(jobs) != 1 {
			continue
		}
		steps := jobs[0].steps
		if jobs[0].load && jobs[0].fail == "" {
			steps = jobs[0].loadSteps // judged per reading, not per frame (aoload.go)
		}
		for _, st := range steps {
			if _, ok := out[st.ID]; ok {
				out[st.ID] = append(out[st.ID], p)
			}
		}
	}
	return out
}

// fill works out what one ticked item runs.
func fill(j *job, plan ptplan.Plan) {
	it := j.item
	human := j.peer == ptitems.PeerHuman
	hasConfirm, hasReading, hasCompanion := false, false, false
	for _, id := range it.Criteria {
		idx := -1
		for i, st := range plan.Steps {
			if st.ID == id {
				idx = i
			}
		}
		if idx < 0 {
			j.fail, j.detail = ReasonNoCriteria, id
			return
		}
		companion := idx > 0 && plan.Steps[idx-1].IsRefCompanion()
		if !forPeer(plan.Steps[idx], companion, j.peer) {
			continue
		}
		if companion && j.peer == ptitems.PeerRef {
			j.steps = append(j.steps, enabled(plan.Steps[idx-1], nil))
			hasCompanion = true
		}
		st := holdFrames(enabled(plan.Steps[idx], j.params))
		hasConfirm = hasConfirm || st.Type == ptplan.TypeUserConfirm
		hasReading = hasReading || st.Type == ptplan.TypeUserReading
		j.steps = append(j.steps, st)
	}
	if j.peer == ptitems.PeerRef && !hasCompanion {
		j.fail = ReasonNoRefStep
		return
	}
	j.record = len(j.steps) == 0
	j.askYesNo = human && it.Human == ptitems.AnswerYesNo && !hasConfirm
	j.askNum = human && it.Human == ptitems.AnswerNumber && !hasReading
	j.askFirst = j.askYesNo && askFirst[it.ID]
	j.askOn = j.askYesNo && j.record && askWhileOn[it.ID]
}

// enabled copies a plan step so it runs in this pick: switched on, with the
// card's parameters over the plan's.
func enabled(st ptplan.Step, params map[string]string) ptplan.Step {
	st.Enabled = nil
	p := ptplan.Params{}
	for k, v := range st.Params {
		p[k] = v
	}
	if !st.OnRef() && (st.Type == ptplan.TypePtSession || st.Type == ptplan.TypePtRun) {
		for k, v := range boardParams(params) {
			p[k] = v
		}
	}
	if len(p) > 0 {
		st.Params = p
	}
	return st
}

// holdFrames stretches an SDRAM retention step to see one whole cycle: the
// card's wait may be longer than the plan's frames cover (decision 151).
func holdFrames(st ptplan.Step) ptplan.Step {
	if st.Type != ptplan.TypePtSession || st.Port != "sdram" {
		return st
	}
	wait, err1 := strconv.Atoi(st.Params["wait"])
	period, err2 := strconv.Atoi(st.Params["period"])
	if err1 != nil || err2 != nil || period <= 0 {
		return st
	}
	// Write, sit, verify, and one more frame that reports the cycle.
	need := (wait+period-1)/period + 3
	if need > st.FrameCount() {
		st.Frames = need
	}
	if floor := need*period + 5000; st.Timeout() < floor {
		st.TimeoutMS = floor
	}
	return st
}

// offered drops the steps whose mode the board does not offer: the demo has
// no sink / source, so the link rates are not measured there (PORTTOOL-FLOW.md
// A.9). A reference-board step is the other board's business.
func offered(steps []ptplan.Step, caps ptproto.Caps) []ptplan.Step {
	var out []ptplan.Step
	for _, st := range steps {
		if mode := st.Params["mode"]; st.Type == ptplan.TypePtSession && !st.OnRef() && mode != "" {
			p, _ := caps.Port(st.Port)
			if lim, ok := p.Limits["mode"]; ok {
				if okv, _ := lim.Accepts(mode); !okv {
					continue
				}
			}
		}
		out = append(out, st)
	}
	return out
}

// continuous says a job can stay running for a whole timed phase and be judged
// frame by frame: one session on the board under test, no peer to open.
func (j *job) continuous() bool {
	if j.record {
		return len(j.item.Targets) == 1 && j.item.Targets[0].Session != "" && !j.askNum
	}
	return len(j.steps) == 1 && j.steps[0].Type == ptplan.TypePtSession &&
		!j.steps[0].OnRef() && j.steps[0].Peer == nil
}

// sessionStart is the pt.start line and frame count of a continuous job.
func (j *job) sessionStart() (cmd string, port string, warmup int) {
	if j.record {
		t := j.item.Targets[0]
		st := ptplan.Step{Port: t.Session, Params: ptplan.Params{}}
		for k, v := range t.Set {
			st.Params[k] = v
		}
		for k, v := range boardParams(j.params) {
			st.Params[k] = v
		}
		return startLine(st), t.Session, 1
	}
	st := j.steps[0]
	return startLine(st), st.Port, st.FrameCount()
}

func startLine(st ptplan.Step) string {
	cmd := "pt.start " + st.Port
	if args := st.ParamArgs(); len(args) > 0 {
		cmd += " " + strings.Join(args, " ")
	}
	return cmd
}

// CheckRD refuses an R&D pick that names the reference board: R&D is done by
// hand (decision 133).
func CheckRD(picks []Pick) error {
	for _, p := range picks {
		if p.On && p.Peer == ptitems.PeerRef {
			return &PickError{Code: ReasonBadPeer, Item: p.ID}
		}
	}
	return nil
}

// Check refuses picks the way Run would, without running anything: unknown
// items, two of one exclusive group, a parameter that is fixed or out of the
// firmware's range.
func Check(items []ptitems.Item, plan ptplan.Plan, caps ptproto.Caps, picks []Pick) error {
	_, err := build(items, plan, caps, picks)
	return err
}
