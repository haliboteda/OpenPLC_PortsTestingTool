package main

// The production side of the one binary: `porttool validate` reads a plan
// without a board, `porttool run` executes one against a board and writes the
// report. The panel and these two share ptcheck, so a limit cannot mean one
// thing on screen and another on the line - see DECISIONS.md 8 and 24.

import (
	"bufio"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"PortTool/src/portmap"
	"PortTool/src/ptboard"
	"PortTool/src/ptplan"
	"PortTool/src/ptreport"
	"PortTool/src/ptseq"
	"PortTool/src/serialx"
	"PortTool/src/simboard"
)

// takesValue says whether a flag consumes the next argument. Boolean flags do
// not, and neither does one already written as --name=value.
func takesValue(fs *flag.FlagSet, arg string) bool {
	name := strings.TrimLeft(arg, "-")
	if strings.Contains(name, "=") {
		return false
	}
	f := fs.Lookup(name)
	if f == nil {
		return false
	}
	b, ok := f.Value.(interface{ IsBoolFlag() bool })
	return !(ok && b.IsBoolFlag())
}

// flagsFirst moves the positional arguments to the end so flags may be written
// on either side of them: Go's flag package stops at the first positional
// argument, and the help writes the plan before the flags.
func flagsFirst(fs *flag.FlagSet, args []string) []string {
	var flags, pos []string

	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			pos = append(pos, args[i+1:]...)
			break
		}
		if !strings.HasPrefix(a, "-") || a == "-" {
			pos = append(pos, a)
			continue
		}
		flags = append(flags, a)
		if takesValue(fs, a) && i+1 < len(args) {
			i++
			flags = append(flags, args[i])
		}
	}
	return append(flags, pos...)
}

// cmdValidate reads a plan and says what is wrong with it, without a board.
func cmdValidate(args []string) int {
	fs := flag.NewFlagSet("validate", flag.ContinueOnError)
	fs.Usage = func() {
		fmt.Fprint(os.Stderr, "porttool validate <plan.json>\n")
	}
	if err := fs.Parse(flagsFirst(fs, args)); err != nil {
		return 2
	}
	if fs.NArg() != 1 {
		fs.Usage()
		return 2
	}

	plan, err := ptplan.Load(fs.Arg(0))
	if err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		return 1
	}
	enabled := 0
	for _, s := range plan.Steps {
		if s.IsEnabled() {
			enabled++
		}
	}
	fmt.Printf("%s (limits %s): %d step(s), %d enabled\n",
		plan.Name, plan.LimitVersion, len(plan.Steps), enabled)
	fmt.Println("The plan is well formed. Ports and parameters are only checked against a board, by `porttool run`.")
	return 0
}

// cmdRun executes a plan against a board.
func cmdRun(args []string) int {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	port := fs.String("port", "", "serial port of the board, e.g. COM7 or /dev/ttyUSB0")
	refPort := fs.String("ref", "", "serial port of the reference board, for steps with on \"ref\"")
	baud := fs.Int("baud", serialx.DefaultBaud, "baud rate of the control port")
	sn := fs.String("sn", "", "serial number to record in the report")
	jsonOut := fs.String("json", "", "write the full report here")
	csvOut := fs.String("csv", "", "write one row per check here")
	yes := fs.Bool("yes", false, "answer every operator prompt with pass")
	fs.Usage = func() {
		fmt.Fprint(os.Stderr, `porttool run <plan.json> --port PORT [options]

  --port PORT      the board's RS232 control port (required), e.g. COM7
                   on Windows, /dev/ttyUSB0 on Linux
  --ref PORT       the reference board's control port, for steps with
                   on "ref"; --port sim --ref sim wires two simulated
                   boards DO to DI
  --baud N         default 115200
  --sn TEXT        serial number to record, and what a ReadSN step with
                   source "arg" reads
  --json FILE      write the full report, raw values included
  --csv FILE       write one row per check
  --yes            answer operator prompts with pass, for an unattended run;
                   it never makes up a number for a reading

Exit code: 0 every step passed, 1 something failed, 2 could not run.

The plan file and the flags may be written in either order.
`)
	}
	if err := fs.Parse(flagsFirst(fs, args)); err != nil {
		return 2
	}
	if fs.NArg() != 1 || *port == "" {
		fs.Usage()
		return 2
	}

	plan, err := ptplan.Load(fs.Arg(0))
	if err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		return 2
	}

	// "sim" runs the plan against the simulated board: it checks the plan and
	// the report, never hardware, and the report records the port name.
	var rw, refRW io.ReadWriteCloser
	if *refPort != "" {
		rw, refRW, err = simboard.OpenPair(*port, *refPort, *baud)
	} else {
		rw, err = simboard.OpenPort(*port, *baud)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "Could not open %s: %v\n", *port, err)
		return 2
	}
	board := ptboard.New(rw, 0)
	defer board.Close()
	var ref *ptboard.Board
	if refRW != nil {
		ref = ptboard.New(refRW, 0)
		defer ref.Close()
	}

	caps, err := board.Caps()
	if err != nil {
		fmt.Fprintf(os.Stderr, `The board on %s did not answer pt.caps: %v

Check the RS232 wiring (C05 TxD / C06 RxD / C02 GND) and that the image was
built with PORTTOOL_ENABLE=1.
`, *port, err)
		return 2
	}

	// Findings that need the board: a port the firmware does not have, a
	// parameter it will not accept, or a limit on a counter that does not
	// travel over the port being judged.
	if findings := plan.CheckAgainstCaps(caps); len(findings) > 0 {
		fmt.Fprintf(os.Stderr, "This plan does not fit the firmware on %s (porttool %s):\n", *port, caps.Version)
		for _, f := range findings {
			fmt.Fprintf(os.Stderr, "  - %s\n", f)
		}
		return 2
	}
	if ref != nil {
		refCaps, err := ref.Caps()
		if err != nil {
			fmt.Fprintf(os.Stderr, "The reference board on %s did not answer pt.caps: %v\n", *refPort, err)
			return 2
		}
		if findings := plan.CheckRefAgainstCaps(refCaps); len(findings) > 0 {
			fmt.Fprintf(os.Stderr, "This plan does not fit the firmware on the reference board %s (porttool %s):\n", *refPort, refCaps.Version)
			for _, f := range findings {
				fmt.Fprintf(os.Stderr, "  - %s\n", f)
			}
			return 2
		}
	} else if plan.HasRefSteps() {
		fmt.Println("No --ref given: the steps that run on the reference board will be ERROR.")
	}

	runner := &ptseq.Runner{
		Board:       board,
		Caps:        &caps,
		Log:         func(s string) { fmt.Println(s) },
		ToolVersion: version,
		SN:          *sn,
		PortName:    *port,
		Confirm:     confirmFunc(*yes),
		Ask:         askFunc(*yes),
		Ref:         ref,
		// A path in a plan is relative to the plan, so a plan and the files it
		// names can be copied to a production PC together.
		BaseDir: filepath.Dir(fs.Arg(0)),
		// Chosen in the panel and recorded beside this program.
		SerialPeer: portmap.Peer,
	}

	fmt.Printf("%s (limits %s) against porttool %s on %s\n\n",
		plan.Name, plan.LimitVersion, caps.Version, *port)

	report, err := runner.Run(plan)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		return 2
	}

	fmt.Printf("\n%s\n", report.Summary())

	if code := writeReports(report, *jsonOut, *csvOut); code != 0 {
		return code
	}
	if !report.Passed() {
		return 1
	}
	return 0
}

func writeReports(report ptreport.Report, jsonPath, csvPath string) int {
	for _, out := range []struct {
		path  string
		write func(io.Writer) error
	}{
		{jsonPath, report.WriteJSON},
		{csvPath, report.WriteCSV},
	} {
		if out.path == "" {
			continue
		}
		f, err := os.Create(out.path)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Could not write %s: %v\n", out.path, err)
			return 2
		}
		err = out.write(f)
		closeErr := f.Close()
		if err == nil {
			err = closeErr
		}
		if err != nil {
			fmt.Fprintf(os.Stderr, "Could not write %s: %v\n", out.path, err)
			return 2
		}
		fmt.Printf("wrote %s\n", out.path)
	}
	return 0
}

// askFunc asks for a number on the terminal, again until it is one. Under
// --yes it refuses: a reading is something a person measured, never a default.
func askFunc(auto bool) func(prompt, unit string) (string, error) {
	if auto {
		return func(prompt, unit string) (string, error) {
			return "", fmt.Errorf("--yes cannot give a reading: %s", prompt)
		}
	}
	in := bufio.NewReader(os.Stdin)
	return func(prompt, unit string) (string, error) {
		for {
			if unit != "" {
				fmt.Printf("\n  %s\n  value in %s: ", prompt, unit)
			} else {
				fmt.Printf("\n  %s\n  value: ", prompt)
			}
			line, err := in.ReadString('\n')
			text := strings.TrimSpace(line)
			if _, perr := strconv.ParseFloat(text, 64); perr == nil {
				return text, nil
			}
			if err != nil {
				return "", fmt.Errorf("could not read a number: %w", err)
			}
			fmt.Printf("  %q is not a number\n", text)
		}
	}
}

// confirmFunc asks on the terminal, or under --yes answers pass for an
// unattended run; --yes is opt-in because a person-only check passing itself
// is worse than no check.
func confirmFunc(auto bool) func(string) (bool, error) {
	if auto {
		return func(prompt string) (bool, error) {
			fmt.Printf("  %s -> pass (--yes)\n", prompt)
			return true, nil
		}
	}
	in := bufio.NewReader(os.Stdin)
	return func(prompt string) (bool, error) {
		fmt.Printf("\n  %s\n  pass? [y/N] ", prompt)
		line, err := in.ReadString('\n')
		if err != nil && line == "" {
			return false, fmt.Errorf("could not read an answer: %w", err)
		}
		answer := strings.ToLower(strings.TrimSpace(line))
		return answer == "y" || answer == "yes", nil
	}
}
