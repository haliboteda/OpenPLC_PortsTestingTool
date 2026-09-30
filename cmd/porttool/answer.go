package main

// `porttool answer` is the station PC being the far end of the link ports.
//
// eth, usb and rs485 are all judged on whether a number the board put on the
// link came back unchanged, so all three need a peer. This is that peer for
// all three at once, because they are one problem wearing three cables.
//
// Directions differ, and that is the only thing this file really has to get
// right: for eth the BOARD listens and this dials in; for usb and rs485 the
// board drives a wire and this opens the serial port at the other end.

import (
	"flag"
	"fmt"
	"net"
	"os"
	"os/signal"
	"strings"
	"time"

	"PortTool/internal/ptecho"
	"PortTool/internal/serialx"
)

func cmdAnswer(args []string) int {
	fs := flag.NewFlagSet("answer", flag.ContinueOnError)
	tcp := fs.String("tcp", "", "board's TCP endpoint for the eth session, e.g. 192.168.0.30:5000")
	coms := fs.String("com", "", "serial port(s) to answer on, comma separated, e.g. COM16")
	usb := fs.Bool("usb", false, "also answer on the board's own CDC port, found by its ST vendor id")
	baud := fs.Int("baud", serialx.DefaultBaud, "baud rate for the serial peers")
	seconds := fs.Int("seconds", 0, "stop after this many seconds (0 = until Ctrl+C)")
	quiet := fs.Bool("quiet", false, "count lines without printing each one")
	fs.Usage = func() {
		fmt.Fprint(os.Stderr, `porttool answer [--tcp HOST:PORT] [--com COM16,COM11] [--usb] [options]

Answers the board on every link the plan tests, by sending each line straight
back. Start it, then start the sessions - or start it alongside a plan run.

  --tcp HOST:PORT  dial the board's eth session (the board listens, this dials)
  --com LIST       serial port(s) to answer on, comma separated
  --usb            also find the board's own CDC port by ST's vendor id
  --baud N         default 115200
  --seconds N      stop after N seconds; 0 means run until Ctrl+C
  --quiet          count lines instead of printing them

At least one of --tcp, --com or --usb is required.

Exit code: 0 every peer answered at least one line, 1 one of them never did,
2 could not start.
`)
	}
	if err := fs.Parse(flagsFirst(fs, args)); err != nil {
		return 2
	}
	if *tcp == "" && *coms == "" && !*usb {
		fs.Usage()
		return 2
	}

	log := func(s string) { fmt.Println(s) }
	if *quiet {
		log = nil
	}

	var peers []*ptecho.Peer
	defer func() {
		for _, p := range peers {
			p.Stop()
		}
	}()

	if *tcp != "" {
		// The board is the server here. Dialling is what proves the listener
		// exists at all, which is the conn=1 the plan asks for.
		conn, err := net.DialTimeout("tcp", *tcp, 8*time.Second)
		if err != nil {
			fmt.Fprintf(os.Stderr, "eth: cannot reach %s: %v\n", *tcp, err)
			return 2
		}
		fmt.Printf("eth      answering %s\n", *tcp)
		peers = append(peers, ptecho.New("eth", conn, log))
	}

	names := []string{}
	for _, c := range strings.Split(*coms, ",") {
		if c = strings.TrimSpace(c); c != "" {
			names = append(names, c)
		}
	}
	if *usb {
		name, err := ptecho.FindCDC()
		if err != nil {
			fmt.Fprintf(os.Stderr, "usb: %v\n", err)
			return 2
		}
		fmt.Printf("usb      found the board's CDC port at %s\n", name)
		names = append(names, name)
	}
	for _, name := range names {
		// RetryOpen because a CDC port appears a moment before the driver will
		// hand it over - the same wait the flashing tools already do.
		port, err := serialx.RetryOpen(name, *baud, 8, 600*time.Millisecond, nil)
		if err != nil {
			fmt.Fprintf(os.Stderr, "%s: cannot open: %v\n", name, err)
			return 2
		}
		fmt.Printf("serial   answering %s at %d baud\n", name, *baud)
		peers = append(peers, ptecho.New(name, port, log))
	}

	fmt.Printf("\n%d peer(s) answering. Start the sessions now.\n\n", len(peers))

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt)
	var deadline <-chan time.Time
	if *seconds > 0 {
		deadline = time.After(time.Duration(*seconds) * time.Second)
	}
	tick := time.NewTicker(5 * time.Second)
	defer tick.Stop()

wait:
	for {
		select {
		case <-sig:
			fmt.Println("\nstopping")
			break wait
		case <-deadline:
			break wait
		case <-tick.C:
			var b strings.Builder
			for i, p := range peers {
				s := p.Stats()
				if i > 0 {
					b.WriteString("   ")
				}
				fmt.Fprintf(&b, "%s rx=%d tx=%d", s.Name, s.Received, s.Echoed)
				if s.Err != "" {
					fmt.Fprintf(&b, " ERR %s", s.Err)
				}
			}
			fmt.Println("  " + b.String())
		}
	}

	// A peer that never answered a line is the failure worth reporting: it
	// means the board never spoke on that link, which is the whole question.
	fmt.Println("\n===== what crossed")
	rc := 0
	for _, p := range peers {
		s := p.Stats()
		state := "ok"
		if s.Echoed == 0 {
			state = "NOTHING CAME BACK"
			rc = 1
		}
		fmt.Printf("  %-12s rx=%-6d tx=%-6d %s %s\n",
			s.Name, s.Received, s.Echoed, state, s.Err)
	}
	return rc
}
