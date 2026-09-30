// PortTool is the hardware engineer's panel for the OpenPLC Bridge terminals.
//
// One file, no install: double-click it and it opens a panel in the browser;
// give it a subcommand and it prints machine-readable output for a production
// sequence or a script. Both modes share this binary so the two can never
// disagree about what a reading means - see DECISIONS.md 7 and 8.
//
// Everything the panel shows comes from the board's own pt.caps reply. There
// is deliberately no list of ports in this program: adding one to the firmware
// is meant to be the only edit needed for it to appear on screen.
package main

import (
	"flag"
	"fmt"
	"os"

	"PortTool/internal/ptpanel"
	"PortTool/internal/serialx"
)

const version = "0.1.0"

var noOpen = flag.Bool("no-browser", false,
	"print the panel's address instead of opening a browser")

func main() {
	flag.Usage = usage
	flag.Parse()

	switch flag.Arg(0) {
	case "ports":
		os.Exit(cmdPorts())
	case "validate":
		os.Exit(cmdValidate(flag.Args()[1:]))
	case "run":
		os.Exit(cmdRun(flag.Args()[1:]))
	case "answer":
		os.Exit(cmdAnswer(flag.Args()[1:]))
	case "version":
		fmt.Printf("PortTool %s\n", version)
	case "":
		// No subcommand is the double-click case: open the panel.
		os.Exit(ptpanel.Serve(!*noOpen, version))
	case "serve":
		os.Exit(ptpanel.Serve(false, version))
	default:
		fmt.Fprintf(os.Stderr, "Unknown command %q\n\n", flag.Arg(0))
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Fprintf(os.Stderr, `PortTool %s - hardware port test panel and production sequence

By hand:
  porttool                 open the panel in a browser
  porttool --no-browser    same, but only print the address
  porttool serve           same as --no-browser
  porttool ports           list the serial ports on this machine

On a line:
  porttool validate PLAN   read a plan file without a board
  porttool run PLAN --port P [--sn S] [--json F] [--csv F]

  porttool answer --tcp HOST:PORT --com COM16 --usb
                           be the far end of the link ports, so eth, usb and
                           rs485 have someone to answer them

  porttool version

The panel listens on 127.0.0.1 only, so nothing outside this machine can
reach it and Windows Firewall has no reason to ask for anything.

`, version)
}

// cmdPorts exists mainly so somebody setting a board up can find out which COM
// number the adapter they just plugged in became, without opening the panel.
func cmdPorts() int {
	ports, err := serialx.List()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Could not list serial ports: %v\n", err)
		return 1
	}
	if len(ports) == 0 {
		fmt.Println("No serial ports found. Plug in the USB-RS232 adapter and try again.")
		return 1
	}
	for _, p := range ports {
		fmt.Println(p.Label())
	}
	return 0
}
