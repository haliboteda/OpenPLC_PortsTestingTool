package ptpanel

import (
	"net/http"

	"PortTool/src/ptcheck"
	"PortTool/src/ptitems"
	"PortTool/src/ptlib"
	"PortTool/src/ptplan"
)

// handleItems is the test-item catalogue as the connected board can run it,
// with each item's criteria resolved against the plan the panel judges by.
// $PROD/docs/modules/M4/PORTTOOL-FLOW.md C.3.6
func (s *Server) handleItems(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	caps := s.caps
	connected := s.board != nil
	s.mu.Unlock()
	if !connected {
		writeJSON(w, 200, map[string]any{"connected": false, "ports": []any{}, "items": []any{}})
		return
	}

	steps := map[string][]ptcheck.Check{}
	var plan ptplan.Plan
	if path, err := safePlanPath(criteriaPlanName()); err == nil {
		if p, err := ptplan.Load(path); err == nil {
			plan = p
			for _, st := range plan.Steps {
				steps[st.ID] = st.Checks
			}
		}
	}

	infos, items := ptitems.ForCaps(caps)
	items = ptlib.ForPlan(items, plan)
	ports := make([]map[string]any, 0, len(infos))
	for _, pi := range infos {
		p, _ := caps.Port(pi.Port)
		ports = append(ports, map[string]any{
			"port": pi.Port, "label": m(pi.Label), "board": p.Board, "term": p.Term,
		})
	}
	out := make([]map[string]any, 0, len(items))
	for _, it := range items {
		crit := make([]map[string]any, 0, len(it.Criteria))
		byPeer := ptlib.CriteriaPeers(it, plan)
		for _, id := range it.Criteria {
			checks, ok := steps[id]
			// peers: which picked peer runs this step, so a configuration
			// card lists only the criteria of its peer (C.3.6 layer 3).
			crit = append(crit, map[string]any{"step": id, "checks": checks, "missing": !ok,
				"peers": byPeer[id]})
		}
		row := map[string]any{
			"id": it.ID, "port": it.Port, "channel": it.Channel,
			"name": m(it.Name), "what": m(it.What),
			"targets": it.Targets, "params": it.Params, "once": it.Once, "once_rd": it.OnceRD,
			"stages": it.Stages, "peers": it.Peers, "rd_peers": it.RDPeers(), "asks": it.Asks(), "default_off": defaultOff(it),
			"exclusive": it.Exclusive, "criteria": crit,
			"ref": it.Ref, "human": it.Human, "live": it.Live, "load": it.Load, "watch_s": it.WatchS, "recover": it.Recover,
		}
		if it.Pending != "" {
			row["pending"] = m(it.Pending)
		}
		if it.Group != "" {
			row["group"] = it.Group
			t := groupText[it.Group]
			row["group_name"], row["group_what"] = m(t[0]), m(t[1])
		}
		out = append(out, row)
	}
	writeJSON(w, 200, map[string]any{"connected": true, "ports": ports, "items": out})
}

// groupText is each AI / AO group card's name and description (decision 114).
var groupText = map[string][2]string{
	"ain.read": {"lib.group.ain.read.name", "lib.group.ain.read.what"},
	"ain.cal":  {"lib.group.ain.cal.name", "lib.group.ain.cal.what"},
	"aout.out": {"lib.group.aout.out.name", "lib.group.aout.out.what"},
	"aout.cal": {"lib.group.aout.cal.name", "lib.group.aout.cal.what"},
}
