package ptpanel

import (
	"bufio"
	"context"
	"net"
	"net/http"
	"os/exec"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// What the panel can say about IP without asking the board.
//
// The board reports its own address in every eth frame. What it cannot know is
// whether this PC can reach it, and that is the question behind almost every
// "conn stays 0" - on 2026-09-10 it cost hours: a proxy adapter held the
// default route so every TCP connect succeeded, the wired NIC was down, and
// the PC was not on the board's segment at all. Nothing on screen said so.

type nic struct {
	Name string `json:"name"`
	IP   string `json:"ip"`
	CIDR string `json:"cidr"`
	Same bool   `json:"same"` // shares a subnet with the board
}

// localNICs lists this machine's usable IPv4 addresses, marking those that
// share a subnet with `board`.
//
// *** All of them, not the "main" one. *** This machine has five, and picking
// one would be picking wrong most of the time: the useful answer is which of
// them can reach the board, and "none of them" is the most useful answer of
// all.
func localNICs(board string) []nic {
	want := net.ParseIP(board)

	ifaces, err := net.Interfaces()
	if err != nil {
		return nil
	}
	var out []nic
	for _, i := range ifaces {
		if i.Flags&net.FlagUp == 0 || i.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := i.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			n, ok := a.(*net.IPNet)
			if !ok || n.IP.To4() == nil {
				continue
			}
			out = append(out, nic{
				Name: i.Name,
				IP:   n.IP.String(),
				CIDR: n.String(),
				// Same subnet means the two can talk without a router, which
				// is the only arrangement a test bench should rely on.
				Same: want != nil && n.Contains(want),
			})
		}
	}
	return out
}

// handleNet answers "can this PC reach that board", for the address the board
// just reported.
func (s *Server) handleNet(w http.ResponseWriter, r *http.Request) {
	board := strings.TrimSpace(r.URL.Query().Get("board"))
	nics := localNICs(board)

	match := ""
	for _, n := range nics {
		if n.Same {
			match = n.IP
			break
		}
	}
	writeJSON(w, 200, map[string]any{
		"board": board,
		"nics":  nics,
		"match": match,
	})
}

// rttPattern pulls a round-trip time out of one line of ping's output without
// depending on what language it speaks. Windows prints "时间=5ms" in Chinese
// and "time=5ms" in English; Linux prints "time=0.123 ms". The number and its
// unit are the part that never moves.
var rttPattern = regexp.MustCompile(`=\s*([0-9]+(?:\.[0-9]+)?)\s*ms`)

// *** Only lines that carry a TTL are replies. *** Matching times across the
// whole output counted ping's own summary as well - "最短 = 1ms，最长 = 6ms，
// 平均 = 2ms" is three more hits - and four pings came back as "7 of 4".
// Every reply line on Windows and on Linux names the TTL and no summary line
// does, so that is the marker, and it is spelled the same in every language.
func replyTimes(text string) []float64 {
	var out []float64
	sc := bufio.NewScanner(strings.NewReader(text))
	for sc.Scan() {
		line := sc.Text()
		if !strings.Contains(strings.ToUpper(line), "TTL=") {
			continue
		}
		m := rttPattern.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		if v, err := strconv.ParseFloat(m[1], 64); err == nil {
			out = append(out, v)
		}
	}
	return out
}

// handlePing pings the board from this PC.
//
// *** The system's ping, not a raw socket. *** Sending ICMP directly needs
// administrator rights on Windows, and a test tool that has to be run as
// administrator is one that will be run some other way instead.
//
// It is worth having at all because it is the cheapest question in the whole
// Ethernet test: it needs no program running on the board, no TCP, no port. If
// ping fails there is no point looking at conn= yet, and if ping works then a
// conn= of zero is about the listener rather than the wiring.
func (s *Server) handlePing(w http.ResponseWriter, r *http.Request) {
	ip := strings.TrimSpace(r.URL.Query().Get("ip"))
	if net.ParseIP(ip) == nil {
		writeErr(w, 400, m("go.ping.bad_ip"))
		return
	}
	count := 4
	if n, err := strconv.Atoi(r.URL.Query().Get("count")); err == nil && n > 0 && n <= 20 {
		count = n
	}

	flag := "-c"
	if runtime.GOOS == "windows" {
		flag = "-n"
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	out, _ := exec.CommandContext(ctx, "ping", flag, strconv.Itoa(count), ip).CombinedOutput()
	text := string(out)

	// Counted from the reply lines rather than from ping's own summary, which
	// is worded differently in every language. The raw output goes back too,
	// so a count that looks wrong can be checked against what ping actually
	// said instead of being argued about.
	hits := replyTimes(text)
	var best, worst, sum float64
	for i, v := range hits {
		if i == 0 || v < best {
			best = v
		}
		if v > worst {
			worst = v
		}
		sum += v
	}
	avg := 0.0
	if len(hits) > 0 {
		avg = sum / float64(len(hits))
	}

	writeJSON(w, 200, map[string]any{
		"ip":       ip,
		"sent":     count,
		"received": len(hits),
		"min_ms":   best,
		"max_ms":   worst,
		"avg_ms":   avg,
		"raw":      strings.TrimSpace(text),
	})
}
