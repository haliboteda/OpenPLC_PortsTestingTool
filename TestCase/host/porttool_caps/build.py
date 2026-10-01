"""Builds and runs the port tool's command dispatcher natively, then checks the
transcript it produced against the protocol contract. Case T4-01.

    python build.py

The point is that pt.caps is what the PC panel is built from: if a caps line
overflows the 192-byte line limit, or advertises a parameter the port does not
report back, or the line count in the header does not match the lines that
follow, the panel silently mis-renders and nobody finds out until a board is on
the bench. All of that is decidable on a PC, so it is decided here.

Compiler, first match wins: $CC, then HOST_CC from config/machine.py, then gcc
or clang on PATH.

Writes the transcript to caps_golden.txt, which is the fixture the Go caps
parser is tested against. Regenerating it is this script running; nobody types
those bytes by hand.

Exit 0 = every check held, 1 = a check failed, 2 = could not build or run.
"""

import re
import subprocess
import sys
from pathlib import Path

HERE = Path(__file__).resolve().parent
sys.path.insert(0, str(HERE.parent.parent / "tools"))

from common import EXE, cfg, Section, Ok, Warn, Fail, read_text  # noqa: E402

BOOT = Path(cfg.BOOT_REPO)
PORTTOOL = BOOT / "TestCase" / "porttool"
# The C harness lives in harness/ rather than beside the Go test: with
# CGO_ENABLED=1 the Go toolchain tries to compile any .c file it finds in a
# package directory, and refuses the package outright.
HARNESS = HERE / "harness"
GOLDEN = HERE / "caps_golden.txt"

failures = []


def check(cond, what, detail=""):
    if cond:
        Ok("PASS  %s" % what)
    else:
        Fail("FAIL  %s%s" % (what, ("  --  " + detail) if detail else ""))
        failures.append(what)
    return cond


def resolve_cc():
    """$CC, HOST_CC, gcc, clang -- first that exists as a path or on PATH."""
    import os
    import shutil
    for cand in (os.environ.get("CC"), getattr(cfg, "HOST_CC", ""), "gcc", "clang"):
        if not cand:
            continue
        if Path(cand).exists():
            return cand
        if shutil.which(cand):
            return cand
    return None


def const_from(path, name, pattern):
    m = re.search(pattern, read_text(path))
    if not m:
        Fail("no %s in %s" % (name, path))
        sys.exit(2)
    return m.group(1)


def parse_kv(line):
    """Splits "OK port=din kind=session ..." into an ordered list of (k, v).

    Deliberately not a dict: a duplicate key is the bug being looked for, and a
    dict would hide it.
    """
    out = []
    for tok in line.split():
        if "=" in tok:
            k, _, v = tok.partition("=")
            out.append((k, v))
    return out


def main():
    cc = resolve_cc()
    if not cc:
        print("No C compiler found. Point HOST_CC in config/machine.py at a gcc.exe.")
        return 2

    line_max = int(const_from(PORTTOOL / "porttool.h",
                              "PORTTOOL_LINE_MAX",
                              r"#define\s+PORTTOOL_LINE_MAX\s+(\d+)"))
    version = const_from(PORTTOOL / "porttool.c",
                         "PORTTOOL_VERSION",
                         r'#define\s+PORTTOOL_VERSION\s+"([^"]+)"')

    Section("build")
    print("  compiler   %s" % cc)
    print("  firmware   %s" % PORTTOOL)
    print("  version    %s   line limit %d" % (version, line_max))

    # --sim builds the same firmware and the same stubs behind a different
    # entry point: a real clock and a stdin/stdout command channel instead of a
    # fixed script. Sharing this source list is the point - a simulated board
    # compiled from a list of its own would drift away from what T4-01 covers.
    sim = "--sim" in sys.argv

    binary = HARNESS / (("porttool_simboard" if sim else "porttool_hosttest") + EXE)
    argv = [
        str(cc), "-std=c11", "-Wall", "-Wextra", "-O0", "-g",
        "-DPORTTOOL_ENABLE=1",
        # Tells testcase_hal_guard.h that this is the host harness, where its
        # "has CubeMX gained this peripheral?" question has no meaning - and
        # where mingw's own <adc.h> makes it answer yes.
        "-DPORTTOOL_HOST_TEST=1",
        "-I", str(HARNESS / "stubs"),
        "-I", str(PORTTOOL),
        "-I", str(BOOT / "TestCase"),
        "-I", str(BOOT / "TestCase" / "common"),
        # Only for IAP_boot_handoff.h, which pt.run reset.cause uses. That
        # header pulls in nothing but stdint/stdbool, and using the real one
        # means a signature change breaks this build instead of drifting.
        "-I", str(BOOT / "IAPServer"),
        "-I", str(BOOT / "Core" / "Inc"),
        str(HARNESS / "stubs" / "hal_stub.c"),
        str(HARNESS / "stubs" / "entries_stub.c"),
        str(HARNESS / "stubs" / "dout_stub.c"),
        str(HARNESS / "stubs" / "analog_stub.c"),
        str(HARNESS / "stubs" / "rs485_stub.c"),
        str(HARNESS / "stubs" / "can_stub.c"),
        str(HARNESS / "stubs" / "knx_stub.c"),
        str(HARNESS / "stubs" / "lwip_stub.c"),
        str(HARNESS / "stubs" / "usb_stub.c"),
        # The real indicator driver, not a stub: it is small, it only needs the
        # GPIO calls the stub HAL already provides, and compiling the real one
        # is what proves pt.run led.blink drives the pin it claims to.
        str(BOOT / "TestCase" / "common" / "port_led.c"),
        str(PORTTOOL / "porttool_cmd.c"),
        str(PORTTOOL / "porttool_din.c"),
        str(PORTTOOL / "porttool_dout.c"),
        str(PORTTOOL / "porttool_ain.c"),
        str(PORTTOOL / "porttool_aout.c"),
        str(PORTTOOL / "porttool_temp.c"),
        str(PORTTOOL / "porttool_rs232.c"),
        str(PORTTOOL / "porttool_rs485.c"),
        str(PORTTOOL / "porttool_relay.c"),
        str(PORTTOOL / "porttool_can.c"),
        str(PORTTOOL / "porttool_knx.c"),
        str(PORTTOOL / "porttool_eth.c"),
        str(PORTTOOL / "porttool_usb.c"),
        str(PORTTOOL / "porttool_run.c"),
        str(PORTTOOL / "porttool_sd.c"),
        str(PORTTOOL / "porttool_sdram.c"),
        str(HARNESS / ("sim_main.c" if sim else "test_main.c")),  # #includes porttool.c
        "-o", str(binary),
    ]
    if sim:
        # -static: a 32-bit libwinpthread-1.dll on the system PATH kills a
        # double-clicked panel's sim. See $PROD/docs/engineering/HOW-TO-RUN-TESTS.md "模拟板".
        argv += ["-pthread", "-static"]
    proc = subprocess.run(argv, stdout=subprocess.PIPE, stderr=subprocess.STDOUT)
    if proc.returncode != 0 or proc.stdout.strip():
        sys.stdout.flush()
        sys.stdout.buffer.write(proc.stdout)
        sys.stdout.buffer.flush()
    if proc.returncode != 0:
        Fail("compile failed")
        return 2
    Ok("compiled clean (-Wall -Wextra)")

    if sim:
        Section("result")
        print("  simulated board: %s" % binary)
        print("  It speaks the port tool protocol on stdin/stdout.")
        print("  Drive it from the panel with:  porttool --sim")
        print("  Or talk to it by hand:         %s" % binary.name)
        print("")
        print("  *** Every reading it gives is invented. It stands in for the")
        print("  *** PC-side wiring, never for a board.")
        return 0

    run = subprocess.run([str(binary)], stdout=subprocess.PIPE,
                         stderr=subprocess.STDOUT)
    if run.returncode != 0:
        sys.stdout.buffer.write(run.stdout)
        Fail("harness exited %d" % run.returncode)
        return 2

    raw = run.stdout.decode("utf-8", "replace")
    GOLDEN.write_text(raw, encoding="utf-8", newline="")

    # Split into (command, [reply lines]) sections.
    sections = []
    for chunk in raw.split(">>> ")[1:]:
        lines = chunk.split("\n")
        cmd = lines[0].rstrip("\r")
        body = [l.rstrip("\r") for l in lines[1:] if l.rstrip("\r") != ""]
        sections.append((cmd, body))

    board_lines = [l for _, body in sections for l in body
                   if not l.startswith("TEST ")]

    # ---------------------------------------------------------------- lines
    Section("line discipline")
    over = [l for l in board_lines if len(l.encode()) > line_max]
    check(not over, "every line within PORTTOOL_LINE_MAX (%d bytes)" % line_max,
          "longest %d bytes: %s" % (max((len(l.encode()) for l in over), default=0),
                                    over[0][:70] if over else ""))
    longest = max(board_lines, key=lambda l: len(l.encode()))
    print("  longest line is %d bytes, %d to spare" %
          (len(longest.encode()), line_max - len(longest.encode())))

    # Plain log text is the protocol's fourth line kind - the bring-up printf a
    # person reads, which ptproto.Classify sorts as LineLog. It is legal, but
    # only from a known source: an unexplained bare line means something is
    # printing where the PC expects a reply, which is how a reply gets split.
    #
    # pt.run is the first thing in this harness that produces any, because the
    # checks it performs print as they go. Matched by shape rather than by a
    # list of names, so a new target does not have to be registered here while
    # a stray printf still fails.
    prose_re = re.compile(r"^[A-Z][A-Z0-9_]*_TEST: ")
    bad_prefix = [l for l in board_lines
                  if not (l.startswith("OK ") or l.startswith("ERR ")
                          or l.startswith("!") or prose_re.match(l))]
    check(not bad_prefix,
          "every line is a reply, a frame, or prose from a named source",
          bad_prefix[0] if bad_prefix else "")

    # ---------------------------------------------------------------- caps
    caps_runs = [body for cmd, body in sections if cmd == "pt.caps"]
    check(len(caps_runs) >= 3, "pt.caps ran at least three times in the transcript")

    def shape_of(run_idx, name):
        """The port= line for one port: kind, terminals, params, running."""
        for l in caps_runs[run_idx][1:]:
            if l.startswith("OK port=%s " % name):
                return dict(parse_kv(l))
        return {}

    def vals_of(run_idx, name):
        """The vals= line for one port: its current parameter values."""
        for l in caps_runs[run_idx][1:]:
            if l.startswith("OK vals=%s " % name):
                return dict(parse_kv(l))
        return {}

    first = caps_runs[0]
    header = parse_kv(first[0])
    hdr = dict(header)
    Section("pt.caps header")
    print("  %s" % first[0])

    check(hdr.get("porttool") == version,
          "header version matches PORTTOOL_VERSION", str(hdr.get("porttool")))
    check("lines" in hdr, "header declares lines=")

    declared = int(hdr.get("lines", -1))
    check(declared == len(first) - 1,
          "lines= matches the lines that follow",
          "declared %d, got %d" % (declared, len(first) - 1))

    port_lines = [l for l in first[1:] if l.startswith("OK port=")]
    check(int(hdr.get("ports", -1)) == len(port_lines),
          "ports= matches the number of port= lines",
          "declared %s, got %d" % (hdr.get("ports"), len(port_lines)))

    # ------------------------------------------------------------ port rows
    Section("pt.caps rows")
    sessions, runs = [], []
    by_kind = {"session": sessions, "run": runs}
    dup_keys = []
    unknown_kind = []
    for line in port_lines:
        kv = parse_kv(line)
        keys = [k for k, _ in kv]
        dups = {k for k in keys if keys.count(k) > 1}
        if dups:
            dup_keys.append((line, dups))
        d = dict(kv)
        bucket = by_kind.get(d.get("kind"))
        if bucket is None:
            unknown_kind.append((d.get("port"), d.get("kind")))
        else:
            bucket.append((line, d, kv))
        print("  %-9s %-9s blk=%s term=%-9s ch=%s loop=%s" %
              (d.get("port"), d.get("kind"), d.get("blk"),
               d.get("term"), d.get("channels"), d.get("loop")))

    check(not dup_keys, "no caps line repeats a key",
          str(dup_keys[0][1]) if dup_keys else "")

    check(not unknown_kind, "every caps row carries a kind the panel knows",
          str(unknown_kind))

    # board= is what the panel groups by. An unknown value is not fatal - the
    # panel shows it verbatim - but it is always a typo in practice, and a
    # silently mistyped one would put a port in a group of its own.
    boards = {"bridge", "upper", "lower", "junction", "whole"}
    bad_board = [(d.get("port"), d.get("board"))
                 for _, d, _ in sessions + runs
                 if d.get("board") not in boards]
    check(not bad_board, "every port names one of the product's boards",
          str(bad_board))

    by_board = {}
    for _, d, _ in sessions + runs:
        by_board.setdefault(d.get("board"), []).append(d.get("port"))
    for b in sorted(by_board):
        print("  %-9s %s" % (b, " ".join(sorted(by_board[b]))))

    required = ("port", "board", "kind", "blk", "term", "channels", "loop")
    missing = [(d.get("port"), f) for _, d, _ in sessions + runs
               for f in required if f not in d]
    check(not missing, "every port row carries the fields the panel needs",
          str(missing[:3]))

    check(all(d["loop"] in ("ctrl", "link", "self", "none")
              for _, d, _ in sessions + runs),
          "loop= is one of ctrl / link / self / none")

    check(all(d.get("channels", "0").isdigit() and int(d["channels"]) >= 1
              for _, d, _ in sessions + runs),
          "channels= is a positive integer everywhere")

    # Current values live on the port's own vals= line: shape and values do not
    # both fit inside the 192-byte limit once a port has a value per channel.
    vals_lines = {}
    for l in first[1:]:
        if l.startswith("OK vals="):
            name = l[len("OK vals="):].split()[0]
            vals_lines[name] = dict(parse_kv(l))

    check(sorted(vals_lines) == sorted(d["port"] for _, d, _ in sessions),
          "every session sent a vals= line and nothing else did",
          "%s vs %s" % (sorted(vals_lines), sorted(d["port"] for _, d, _ in sessions)))

    # A session must report a current value for every parameter it advertises,
    # otherwise the panel renders a control it cannot show the state of.
    for line, d, kv in sessions:
        params = [p for p in d.get("params", "").split(",") if p]
        reported = vals_lines.get(d["port"], {})
        absent = [p for p in params if p not in reported]
        check(not absent,
              "%s reports a value for each of params=%s" % (d["port"], d.get("params")),
              "missing %s" % absent)

    # ------------------------------------------------- run rows vs pt.run
    Section("run targets")

    check(all("runs" in d and d["runs"] for _, d, _ in runs),
          "every run row lists its runs")

    run_listed = []
    for cmd, body in sections:
        if cmd == "pt.run":
            run_listed = [l.split("=", 1)[1].split()[0] for l in body
                          if l.startswith("OK run=")]

    # A session row may carry runs= too: eth's session is the TCP server and
    # eth.link is the PHY probe, one RJ45 between them (DECISIONS.md 28). Both
    # kinds of row have to be swept, or a target parked on a session row looks
    # like one the panel can never reach.
    in_caps = []
    for _, d, _ in runs + sessions:
        if d.get("runs"):
            in_caps.extend(d["runs"].split(","))

    # Every pt.run target has to be in caps, because that list is what lets the
    # PC check a plan file's targets with no board attached. A target reachable
    # only by typing the command would be one a plan can name and no check can
    # catch.
    print("  pt.run lists %d targets; caps offers %d" % (len(run_listed), len(in_caps)))
    check(sorted(run_listed) == sorted(in_caps),
          "every pt.run target appears in caps and nothing else does",
          "only in list: %s / only in caps: %s" %
          (sorted(set(run_listed) - set(in_caps)),
           sorted(set(in_caps) - set(run_listed))))

    # One row per piece of hardware: two rows with the same port= would give
    # the panel two cards for one chip (DECISIONS.md 17).
    all_ports = [d["port"] for _, d, _ in sessions + runs]
    dup_ports = sorted({p for p in all_ports if all_ports.count(p) > 1})
    check(not dup_ports, "no two caps rows share a port name", str(dup_ports))

    # ------------------------------------------------------------- lifecycle
    Section("session lifecycle")
    running_before = shape_of(0, "din")
    running_during = shape_of(1, "din")
    running_after = shape_of(2, "din")
    vals_during = vals_of(1, "din")
    check(running_before["running"] == "0", "din starts not running")
    check(running_during["running"] == "1", "din reports running=1 once started")
    check(running_after["running"] == "0", "din reports running=0 after pt.stop all")
    check(vals_during.get("ch") == "1,3,5",
          "caps echoes back the channels pt.start was given",
          str(vals_during.get("ch")))

    energised = ""
    init_counts = ""
    for _, body in sections:
        for l in body:
            if l.startswith("TEST relay_energised="):
                energised = l.split("=", 1)[1]
            if l.startswith("TEST relay_init_count="):
                init_counts = l
    check(energised != "" and set(energised) == {"0"},
          "pt.stop all released every relay", "energised=%s" % energised)
    print("  %s" % init_counts)

    # ------------------------------------------- the command port going deaf
    Section("receive errors on the command port")

    # A real board failure, 2026-09-08: the HAL does not clear a receive error
    # for a zero-timeout HAL_UART_Receive, so the flag latched and the board
    # heard no further commands while its sessions kept pushing frames. The
    # firmware clears it itself now, and this is what holds that in place.
    rx = {}
    for _, body in sections:
        for l in body:
            if l.startswith("TEST rx_before=") or l.startswith("TEST rx_after="):
                rx.update(dict(parse_kv(l)))

    check(rx.get("rx_before") == "0" and rx.get("rx_after") == "1",
          "a raised receive error is counted, not ignored",
          "before=%s after=%s" % (rx.get("rx_before"), rx.get("rx_after")))

    # 0xE is ORECF | NECF | FECF - bits 3, 2, 1, the real positions. Writing
    # some other value would leave a flag latched and the deafness with it.
    check(rx.get("icr_written", "").upper() == "0XE",
          "all three receive-error flags are cleared",
          "ICR=%s want 0xE (ORECF|NECF|FECF)" % rx.get("icr_written"))

    # 32 is HAL_UART_STATE_READY. A handle left BUSY_RX would make every later
    # HAL_UART_Receive return HAL_BUSY - the same deafness by another route.
    check(rx.get("state") == "32" and rx.get("error_code") == "0",
          "the handle is left ready with no error code",
          "state=%s error_code=%s" % (rx.get("state"), rx.get("error_code")))

    # And the receive path end to end: a command fed one byte at a time through
    # the real ISR has to come out of poll_command as a dispatched command.
    # This is the path that used to lose bytes while printf was blocking, so a
    # reply here is the evidence that it no longer has to be watched to be
    # caught.
    fed_reply = ""
    for cmd, body in sections:
        if "one byte at a time" not in cmd:
            continue
        for l in body:
            if l.startswith("OK uid="):
                fed_reply = l
    check(fed_reply != "",
          "a command delivered through the ISR is dispatched",
          "no reply followed the fed bytes")

    # rx_dropped is the ring overflowing. Anything but zero here means the
    # drain fell behind inside a test that feeds six bytes, which would make
    # every number above suspect.
    check("rx_dropped=0" in fed_reply,
          "the receive ring did not overflow",
          fed_reply)

    # ------------------------------------------- a frequency per DO channel
    #
    # The timer channels these eight pins sit on pair up on four compare units,
    # and each pair is complementary - hardware PWM could never give eight
    # independent frequencies. The software PWM does, and this is where that
    # claim is decided.
    Section("Digital Out: a frequency per channel")
    # caps carries the per-channel frequency, not the frame: a frame with both
    # for eight channels runs past the line limit, and a plan's "ch1 eq 100"
    # would have to know the frequency to still pass.
    dvals = [l for _, body in sections for l in body
             if l.startswith("OK vals=dout ")]
    three = [l for l in dvals if "freq=1:2000,2:1000,3:250" in l]
    check(three, "caps reports three different frequencies at once",
          " / ".join(dvals[-3:])[:200])
    check(any("freq=1:500,2:500,3:500" in l for l in dvals),
          "one value with no colon still sets every selected channel",
          " / ".join(dvals[-3:])[:200])

    # The widest spread the parameter allows, and where the reported frequency
    # is quantised hardest. 1999 and 0 is what truncation produced on the board
    # on 2026-09-10; a channel reporting 0 Hz while it switches reads as dead.
    check(any("freq=1:2000,2:1," in l for l in dvals),
          "2000 Hz beside 1 Hz comes back as 2000 and 1, not 1999 and 0",
          " / ".join(l for l in dvals if "2:1," in l or "1999" in l)[:200])

    # The interrupt rate is the fastest channel times the duty resolution, and
    # it is the resolution behind every frequency above - so the frame says it
    # rather than leaving it to be inferred.
    dframes = [dict(parse_kv(l)) for _, body in sections for l in body
               if l.startswith("!dout ") and "ch1=50 " in (l + " ")]
    ticks = [f.get("tick") for f in dframes]
    check("200000" in ticks,
          "and the interrupt rate follows the fastest of them", str(ticks))
    check("50000" in ticks,
          "then drops with them - it is not left at the peak", str(ticks))
    # A duty stays a plain number, or every existing criterion on it breaks.
    check(all("@" not in (f.get("ch1") or "") for f in dframes),
          "the duty in a frame is still a plain number",
          str([f.get("ch1") for f in dframes]))

    # Every channel, not only the selected ones. The panel fills one control
    # per channel from this line, and a channel missing from it becomes a zero
    # - which is below the frequency floor, so the next command the panel sends
    # after somebody ticks that channel is refused outright. Caught against the
    # board by case T4-02 on 2026-09-10.
    short = [l for l in dvals if dict(parse_kv(l)).get("freq", "").count(":") != 8]
    check(not short,
          "caps lists a frequency for all eight outputs, whatever is selected",
          (short[0] if short else "")[:170])

    # A per-channel frequency has to be refused the same way a per-channel duty
    # is: naming an output that does not exist, or a rate the timer will not
    # take, must take the whole line down rather than half-applying.
    for cmd, expect in (
        ("pt.start dout freq=1:99999", "1..2000 Hz"),
        ("pt.start dout freq=9:500", "outputs 1..8"),
    ):
        got = [l for c, body in sections if c == cmd for l in body]
        check(any(l.startswith("ERR") and expect in l for l in got),
              "refused with the reason: %s" % cmd,
              " / ".join(got)[:160])

    # ------------------------------------------- which clock the RTC counts
    #
    # This field was the literal "lsi" until the project moved to LSE, at which
    # point it went on saying lsi against a board with a crystal. A field that
    # cannot disagree with the hardware is not a reading, and the only way to
    # tell the two apart is to move the register and watch.
    Section("rtc.read names the oscillator it actually found")
    rtcs = [dict(parse_kv(l)) for _, body in sections for l in body
            if l.startswith("OK rtc.read ")]
    seen = [f.get("clk") for f in rtcs]
    for want in ("lse", "lsi", "none"):
        check(want in seen, "RTCSEL=%s comes back as clk=%s" % (want, want), str(seen))
    check(seen and seen[-1] == "lse",
          "and it follows the register back, not just away from the default",
          str(seen))

    # ------------------------------------------------ the card detect switch
    #
    # Hot-plug is the one thing about the SD slot that normally needs a person
    # with the card in their hand. Here the detect pin is a variable, so it is
    # decidable - and the property being pinned down is that an insertion is
    # COUNTED, not just visible as a level. A step that only sees detected=1
    # cannot tell a card put in during the test from one already there.
    Section("SD hot-plug")
    sdf = [dict(parse_kv(l)) for _, body in sections for l in body
           if l.startswith("!sd ")]
    want = [
        ("1", "0", "0", "0", "a card sitting in the slot is no edge at all"),
        ("0", "1", "0", "1", "pulling it out is seen, and counted as a removal"),
        ("1", "2", "1", "1", "putting it back is counted as an insertion"),
        ("1", "4", "2", "2", "out and in between two frames loses neither edge"),
        ("1", "0", "0", "0", "starting again zeroes the counters"),
    ]
    check(len(sdf) >= len(want), "the transcript stages %d hot-plug frames" % len(want),
          "got %d" % len(sdf))
    for i, (det, ch, ins, outs, why) in enumerate(want):
        if i >= len(sdf):
            break
        f = sdf[i]
        got = (f.get("detected"), f.get("changes"), f.get("in"), f.get("out"))
        check(got == (det, ch, ins, outs), "frame %d - %s" % (i + 1, why),
              "detected/changes/in/out = %s, want %s"
              % ("/".join(map(str, got)), "/".join((det, ch, ins, outs))))

    # ------------------------------------------------------- the encoders
    #
    # Four encoders on the eight digital-in pins. The transcript drives the A/B
    # states directly, so what is being judged is the decoding: direction,
    # that the pairs are independent, and that a transition with no direction
    # in it is reported rather than guessed at.
    Section("quadrature decoding")
    QUAD_START = "pt.start din ch=1,2,3,4,5,6,7,8 mode=quad period=1000"
    qat = next((i for i, (cmd, _) in enumerate(sections) if cmd == QUAD_START), None)
    if check(qat is not None, "the transcript drives the encoders"):
        quad = []
        for _, body in sections[qat:]:
            for l in body:
                if l.startswith("!din ") and "mode=quad" in l:
                    quad.append(dict(parse_kv(l)))
        # d1 is the last direction seen, not the direction right now, so it
        # stays where it was through frames that moved nothing.
        want = [
            ("0", "0", "0", "0", "a session that just started has counted nothing"),
            ("4", "1", "0", "0", "four states forward is four counts up"),
            ("0", "-1", "0", "0", "and the same four back is four down, not eight more"),
            ("0", "-1", "0", "1", "both phases at once has no direction, so it is a miss"),
            # err is 2 by then, not 1: coming back out of the state that was
            # jumped into is a jump as well, and pretending otherwise would
            # mean a decoder that reports half the steps it missed.
            ("0", "-1", "4", "2", "encoder 2 moves on its own pins, encoder 1 stays put"),
        ]
        check(len(quad) >= len(want),
              "the transcript produced %d encoder frames" % len(want),
              "got %d" % len(quad))
        for i, (g1, d1, g2, err, why) in enumerate(want):
            if i >= len(quad):
                break
            f = quad[i]
            got = (f.get("g1"), f.get("d1"), f.get("g2"), f.get("err"))
            check(got == (g1, d1, g2, err), "encoder frame %d - %s" % (i + 1, why),
                  "g1/d1/g2/err = %s, want %s" % ("/".join(map(str, got)),
                                                  "/".join((g1, d1, g2, err))))
        check(all("v" in f for f in quad),
              "an encoder frame still carries the raw pins as v=")
        # The per-channel pairs are deliberately absent here: with all eight of
        # them next to four counters the line runs past PORTTOOL_LINE_MAX and
        # gets truncated into something no parser can read. v= loses nothing.
        check(not any(k.startswith("ch") for f in quad for k in f),
              "and does NOT repeat them one per channel - the line would not fit")

    # ------------------------------------------------------------ echo loop
    Section("echo loop")
    # From the command that starts that section onwards, not from the top of
    # the transcript: din frames appear elsewhere too, and taking "the first
    # six" made this section fail the moment another test in front of it
    # started one - a break in a check that had nothing to do with the change.
    ECHO_START = "pt.start din ch=1 period=200"
    at = next((i for i, (cmd, _) in enumerate(sections) if cmd == ECHO_START), None)
    check(at is not None, "the echo section still starts with " + ECHO_START)
    frames = []
    for _, body in sections[at if at is not None else 0:]:
        for l in body:
            if l.startswith("!din "):
                frames.append(dict(parse_kv(l)))

    # The whole point is that these three numbers tell the loop's three states
    # apart from the frames alone, which is all the panel ever sees.
    want = [
        ("1", "0", "0", "first frame: nothing to have answered yet"),
        ("2", "1", "0", "the PC answered, so seq advances from what came back"),
        ("3", "2", "0", "and again"),
        ("3", "2", "1", "no answer: miss counts, seq must NOT advance"),
        ("3", "2", "2", "still none"),
        ("3", "2", "3", "a stale answer is a miss, not a closed loop"),
    ]
    check(len(frames) >= len(want),
          "the transcript produced %d echo frames" % len(want),
          "got %d" % len(frames))
    for i, (seq, rx, miss, why) in enumerate(want):
        if i >= len(frames):
            break
        f = frames[i]
        got = (f.get("seq"), f.get("rx"), f.get("miss"))
        check(got == (seq, rx, miss), "frame %d - %s" % (i + 1, why),
              "seq/rx/miss = %s, want %s" % ("/".join(map(str, got)),
                                             "/".join((seq, rx, miss))))

    # A frame must still carry the full bit field next to the counter: the
    # counter is about the link, the bit field is about the pins.
    check(all("v" in f for f in frames),
          "every echo frame still carries the whole v= bit field")

    for cmd, expect in (
        ("pt.echo relay 1", "is not running"),
        ("pt.echo nosuch 1", "no such port"),
        ("pt.echo din notanumber", "is not a number"),
        # can became a loop=link session on 2026-09-08. Before that this
        # answered "no such port" - which passed for the wrong reason: what
        # the line is here to check is that a LINK port refuses an echo
        # offered on the control port.
        ("pt.echo can 1", "takes its echo on its own link"),
    ):
        body = [b for c, b in sections if c == cmd]
        got = body[0][0] if body and body[0] else "(nothing)"
        check(got.startswith("ERR ") and expect in got,
              "refused: %s" % cmd, got)

    # ------------------------------------------------------- rs485 as a link
    Section("rs485: the first loop=link port")

    def reply_first(cmd):
        body = [b for c, b in sections if c == cmd]
        return body[0][0] if body and body[0] else "(nothing)"

    rs485_frames = [dict(parse_kv(l)) for _, body in sections for l in body
                    if l.startswith("!rs485 ")]
    check(len(rs485_frames) >= 4,
          "rs485 produced the four frames the sequence needs",
          "got %d" % len(rs485_frames))

    if len(rs485_frames) >= 4:
        # Same three states as any echo loop, but here the reply travelled over
        # the pair being tested, so the counter is the verdict on that pair.
        want = [
            ("1", "0", "0", "first send: nothing to have answered yet"),
            ("2", "1", "0", "the far end answered on the pair"),
            ("2", "1", "1", "silence on the pair: miss climbs, seq holds"),
            ("2", "1", "2", "something that is not an answer is still a miss"),
        ]
        for i, (seq, rx, miss, why) in enumerate(want):
            f = rs485_frames[i]
            got = (f.get("seq"), f.get("rx"), f.get("miss"))
            check(got == (seq, rx, miss), "rs485 frame %d - %s" % (i + 1, why),
                  "seq/rx/miss = %s" % "/".join(map(str, got)))

        last = rs485_frames[-1]
        check(last.get("junk") == "1",
              "a reply that was not a number is counted, not ignored",
              "junk=%s" % last.get("junk"))
        check(int(last.get("rxbytes", "0")) > 0,
              "the receiver reports the bytes it took off the pair",
              "rxbytes=%s" % last.get("rxbytes"))

    sent = ""
    for _, body in sections:
        for l in body:
            if l.startswith("TEST rs485_sent="):
                sent = l[len("TEST rs485_sent="):].split(" driving=")[0]
    check(sent.startswith("1|"),
          "the board put the sequence number on the pair, one line at a time",
          "sent=%r" % sent)
    check(" driving=0" in "".join(l for _, b in sections for l in b
                                  if l.startswith("TEST rs485_sent=")),
          "the driver is left off, so the session does not hold the pair")

    # The firmware must refuse pt.echo here. Answering on the control port
    # would let this counter climb with the pair dead - the false pass the
    # whole loop= distinction exists to prevent.
    got = reply_first("pt.echo rs485 2")
    check(got.startswith("ERR ") and "own link" in got,
          "pt.echo is refused for a link port", got)
    check(reply_first("pt.start rs485 baud=12345").startswith("ERR "),
          "an unsupported baud rate is refused",
          reply_first("pt.start rs485 baud=12345"))

    def ok_line(body):
        """The OK line out of a body the check's own prose shares."""
        oks = [l for l in body if l.startswith("OK ")]
        return oks[0] if oks else "(no OK line)"

    # pt.run rs485.pins, three times in this order: while the session owns the
    # pins, with nothing running, and with PD4 wedged low. All three replies
    # are needed - a check only ever seen passing proves nothing about what it
    # would do when the board is actually broken.
    pins = [ok_line(b) for c, b in sections if c == "pt.run rs485.pins" and b]
    check(len(pins) == 3,
          "pt.run rs485.pins answered all three times", "got %d" % len(pins))
    if len(pins) == 3:
        busy, healthy, stuck = (dict(parse_kv(l)) for l in pins)

        # pt.run leaves sessions alone, so the only honest move here is to
        # decline: re-muxing PD4 under a live session would break it and the
        # session would go on reporting misses as if the pair were at fault.
        check(busy.get("checked") == "0" and busy.get("busy") == "1",
              "declined while the rs485 session holds the pins", pins[0])

        check(healthy.get("follows") == "1" and healthy.get("dir_high") == "1"
              and healthy.get("tx_high") == "1"
              and healthy.get("dir_low") == "0" and healthy.get("tx_low") == "0",
              "both pins read back what was written to them", pins[1])

        # The point of the whole target. A direction pin that cannot go high
        # means the transceiver can never transmit, and that has to be
        # distinguishable from "nobody on the far end".
        check(stuck.get("follows") == "0" and stuck.get("dir_high") == "0",
              "a pin that will not follow is reported as such, and named",
              pins[2])
        check(stuck.get("tx_high") == "1",
              "the pin that still works is not dragged down with it", pins[2])

    # rs232 is the one port whose control-port round trip IS the link test.
    got = reply_first("pt.echo rs232 1")
    check(got.startswith("OK "),
          "pt.echo is accepted for rs232, whose link is the control port", got)

    # -------------------------------------------------------- analog gate
    Section("analog: the reference gate")

    def reply_to(cmd):
        body = [b for c, b in sections if c == cmd]
        return body[0][0] if body and body[0] else "(nothing)"

    # With no reference, all three must refuse rather than report numbers that
    # look real. This is the one analog failure a PC can decide.
    for cmd in ("pt.start ain", "pt.start aout mv=500", "pt.start temp"):
        got = reply_to(cmd)
        check(got.startswith("ERR ") and "VREFBUF" in got,
              "refused with no reference: %s" % cmd, got)

    listed = [b for c, b in sections if c == "pt.list"]
    check(listed and listed[-1] and listed[-1][0] == "OK running=none",
          "nothing was left running after the three refusals",
          str(listed[-1] if listed else None))

    # With the reference up they start, and the frame carries all three of
    # asked / quantised / microamps - the request alone would hide a rounding
    # of several millivolts, the quantised value alone would look like the
    # panel ignored what was typed.
    aout_frames = [l for _, body in sections for l in body if l.startswith("!aout ")]
    check(aout_frames, "aout produced a frame once the reference was up")
    if aout_frames:
        f = dict(parse_kv(aout_frames[-1]))
        parts = (f.get("ch1") or "").split("/")
        check(len(parts) == 3,
              "aout reports asked/quantised/microamps", f.get("ch1"))
        if len(parts) == 3:
            check(parts[0] == "500", "the asked value is what was asked", parts[0])
            check(parts[1] != parts[0],
                  "the quantised value differs from the request, as a 12-bit DAC must",
                  "/".join(parts))
            check(int(parts[2]) > 0, "a current is computed for the terminal", parts[2])

    ain_frames = [l for _, body in sections for l in body if l.startswith("!ain ")]
    check(ain_frames, "ain produced a frame")
    if ain_frames:
        f = dict(parse_kv(ain_frames[-1]))
        check(f.get("ok") == "1", "ain says its reference is trustworthy", str(f.get("ok")))
        check("/" in (f.get("ch1") or ""), "ain reports raw/millivolts", str(f.get("ch1")))

    for cmd in ("pt.start aout mv=9999", "pt.start aout mv=1:4000", "pt.start ain ch=3"):
        check(reply_to(cmd).startswith("ERR "), "refused: %s" % cmd, reply_to(cmd))

    dac_after = ""
    for _, body in sections:
        for l in body:
            if l.startswith("TEST dac_after_stop="):
                dac_after = l.split("=", 1)[1]
    check(dac_after == "0,0",
          "pt.stop all put both analog outputs back to zero",
          "dac=%s" % dac_after)

    # ------------------------------------------------- per-channel levels
    Section("per-channel levels")

    after_set = vals_of(3, "relay")
    check(after_set.get("on") == "1:1,2:0,3:1",
          "relay took a level per channel", str(after_set.get("on")))
    after_one = vals_of(4, "relay")
    check(after_one.get("on") == "1:1,2:1,3:1",
          "pt.set changed only the channel it named", str(after_one.get("on")))

    # The four malformed on= lines must all be refused, and the state that
    # follows them must be the state that preceded them.
    for cmd in ("pt.start relay on=1:9", "pt.start relay on=7:1",
                "pt.start relay on=1:1,1:0", "pt.start relay on=1:"):
        body = [b for c, b in sections if c == cmd]
        got = body[0][0] if body and body[0] else "(nothing)"
        check(got.startswith("ERR "), "refused: %s" % cmd, got)
    unchanged = vals_of(5, "relay")
    check(unchanged.get("on") == after_one.get("on"),
          "four refused on= lines left the levels exactly as they were",
          "%s -> %s" % (after_one.get("on"), unchanged.get("on")))

    # A line refused on its second parameter must not have applied its first.
    before = vals_of(5, "din").get("ch")
    after = vals_of(6, "din").get("ch")
    check(after == before,
          "a valid ch= in a line refused for its period= did not take effect",
          "%s -> %s" % (before, after))

    # ------------------------------------------------------------- refusals
    Section("refusals")
    for cmd, expect in (
        ("pt.start din ch=1,9", "start refused"),
        ("pt.start din period=abc", "start refused"),
        ("pt.start dinn", "no such port"),
        ("pt.set rtc ch=1", "no such port"),
        ("pt.foo", "unknown command"),
    ):
        body = [b for c, b in sections if c == cmd]
        got = body[0][0] if body and body[0] else "(nothing)"
        check(got.startswith("ERR ") and expect in got,
              'refused: %s' % cmd, got)

    # A refusal must not have started anything.
    check(vals_of(0, "din").get("ch") != "1,9",
          "a rejected ch= never reached the session")

    # -------------------------------------------------- one-shot actions
    Section("pt.run: one-shot actions")

    run_bodies = [b for c, b in sections if c == "pt.run sdram.probe"]
    check(len(run_bodies) == 2,
          "pt.run sdram.probe answered both times it was asked",
          "%d section(s)" % len(run_bodies))

    catalogue = [b for c, b in sections if c == "pt.run"]
    check(catalogue and all(l.startswith("OK run=") for l in catalogue[0]),
          "bare pt.run lists the targets, one OK per target",
          str(catalogue[0][:2] if catalogue else None))

    if len(run_bodies) == 2:
        for label, raw_body, want_ready in (("a good board", run_bodies[0], "1"),
                                            ("an FMC that never came up", run_bodies[1], "0")):
            # "TEST ..." lines are the harness observing the stub, not bytes the
            # board put on the wire.
            body = [l for l in raw_body if not l.startswith("TEST ")]
            oks = [l for l in body if l.startswith("OK ")]
            check(len(oks) == 1,
                  "%s: exactly one OK line" % label,
                  "%d in %s" % (len(oks), body))
            if not oks:
                continue

            # The checks print prose as they go, and the OK line still has
            # to arrive whole and last.
            check(body[0].startswith("SDRAM_TEST:"),
                  "%s: the check's own prose comes first" % label, body[0])
            check(body[-1] == oks[0],
                  "%s: the OK line is last, not split by the prose" % label, str(body))

            f = dict(parse_kv(oks[0]))
            check(f.get("ready") == want_ready,
                  "%s: ready=%s" % (label, want_ready), str(f.get("ready")))
            check(f.get("size") == str(0x04000000),
                  "%s: reports the window size in bytes" % label, str(f.get("size")))

            # No verdict anywhere in the reply - DECISIONS.md 22.
            check("PASS" not in oks[0] and "FAIL" not in oks[0],
                  "%s: the reply carries measurements, not a verdict" % label, oks[0])

    # sdram.crc, three windows: the default, one the plan named, and one that
    # runs off the end of the array.
    crcs = [ok_line(b) for c, b in sections
            if c.startswith("pt.run sdram.crc") and b]
    check(len(crcs) == 3, "pt.run sdram.crc answered all three times",
          "got %d" % len(crcs))
    if len(crcs) == 3:
        whole, window, past_end = (dict(parse_kv(l)) for l in crcs)

        check(whole.get("offset") == "0" and whole.get("bytes") == "67108864",
              "no window given means the whole array", crcs[0])
        check(window.get("offset") == "1024" and window.get("bytes") == "4096",
              "the plan's window is what gets summed, and is said back",
              crcs[1])

        # An offset past the mapping wraps on this part, so a CRC taken there
        # would be a number that looks like an answer and is not one.
        check(past_end.get("offset") == "0",
              "an offset past the end of the array is pulled back inside",
              crcs[2])
        check(past_end.get("bytes") == "67108864",
              "a length past the end is clamped, not wrapped", crcs[2])

        check(window.get("crc") != whole.get("crc"),
              "a different window gives a different CRC", crcs[1])

    # reset.cause. The PC can already see THAT a board restarted - a frame's
    # tick falls back to near zero - so what this has to add is why, and the
    # two causes below are the ones that decide who owns the failure: PIN is
    # somebody knocking the reset line during an ageing run, IWDG is the board
    # hanging and its own watchdog rescuing it.
    causes = [ok_line(b) for c, b in sections if c == "pt.run reset.cause" and b]
    check(len(causes) == 2, "pt.run reset.cause answered both times",
          "got %d" % len(causes))
    if len(causes) == 2:
        pin, iwdg = (dict(parse_kv(l)) for l in causes)
        check(pin.get("cause") == "PIN", "reports the decoded cause", causes[0])
        check(iwdg.get("cause") == "IWDG",
              "and follows the register rather than answering from memory",
              causes[1])
        # The raw word too: the name stops at the first flag it matches, and
        # two causes can be set at once.
        check(pin.get("rsr") == "0x04000000" and iwdg.get("rsr") == "0x20000000",
              "the raw RSR goes out beside the name",
              "%s / %s" % (pin.get("rsr"), iwdg.get("rsr")))

    # The XTR111 fault flags. They are the only output-fault signal on this
    # board that reaches the MCU, so an ageing run has nothing else to watch -
    # and the frame has to carry the pin level as it reads, because which level
    # means fault is not settled anywhere in Hardware/ yet.
    aout_frames = [dict(parse_kv(l)) for _, body in sections for l in body
                   if l.startswith("!aout ")]
    check(aout_frames, "aout produced frames")
    if aout_frames:
        check(all("ef1" in f and "ef2" in f for f in aout_frames),
              "every aout frame carries both fault flags", str(aout_frames[0]))
        # One high and one low at the same moment: a firmware reading one pin
        # twice would report them equal, and PI4/PE3 are different ports.
        staged = [f for f in aout_frames if f.get("ef1") == "1"]
        check(staged, "a staged fault on channel 1 reached the frame",
              str([f.get("ef1", "") + "/" + f.get("ef2", "") for f in aout_frames]))
        if staged:
            check(staged[-1].get("ef2") == "0",
                  "and the other channel was not dragged with it", str(staged[-1]))

    check(reply_to("pt.run nosuch").startswith("ERR "),
          "an unknown run target is refused", reply_to("pt.run nosuch"))

    # The RTC is not up in this image either, so the target has to bring it up
    # before it can read anything.
    rtc_bodies = [b for c, b in sections if c == "pt.run rtc.read"]
    check(len(rtc_bodies) >= 2, "pt.run rtc.read answered at least twice",
          "%d section(s)" % len(rtc_bodies))
    inits = [l for _, body in sections for l in body
             if l.startswith("TEST rtc_init_count=")]
    check(inits and inits[0].endswith("=1"),
          "rtc.read brought the RTC up itself, exactly once",
          inits[0] if inits else "(no observation)")
    if len(rtc_bodies) >= 2:
        for label, body, want_init in (("a set calendar", rtc_bodies[0], "1"),
                                       ("a calendar nobody set", rtc_bodies[1], "0")):
            ok = [l for l in body if l.startswith("OK ")]
            check(len(ok) == 1, "%s: one OK line" % label, str(body))
            if not ok:
                continue
            f = dict(parse_kv(ok[0]))
            check(f.get("init") == want_init,
                  "%s: init=%s" % (label, want_init), str(f.get("init")))
            # The clock source is a fact the PC has to know to judge drift.
            # Which oscillator it names is checked where the register is moved;
            # here all that matters is that the field is there and populated.
            check(f.get("clk") in ("lse", "lsi", "hse", "none"),
                  "%s: names the clock source" % label, str(f.get("clk")))
            check("time" in f and "date" in f,
                  "%s: reports a date and a time" % label, ok[0])

    led = [b for c, b in sections if c == "pt.run led.blink"]
    check(led, "pt.run led.blink answered")
    if led:
        ok = [l for l in led[0] if l.startswith("OK ")]
        check(len(ok) == 1, "led.blink: one OK line", str(led[0]))
        if ok:
            f = dict(parse_kv(ok[0]))
            # There is no readback on this pin, so the reply must not claim one.
            check(f.get("observed") == "unknown",
                  "led.blink does not claim the lamp was seen", ok[0])
            check(f.get("pin") == "PE2", "led.blink names the pin", str(f.get("pin")))
    drives = [l for _, body in sections for l in body
              if l.startswith("TEST led_configured=")]
    if drives:
        d = dict(parse_kv(drives[0]))
        check(d.get("led_configured") == "1",
              "led.blink configured PE2 itself (MX_GPIO_Init leaves it alone)",
              drives[0])
        # Six pulses is twelve writes, half of them high. The counters are
        # zeroed just before the run because the pt.led checks above drive the
        # same indicator; PortLed_Init()'s one extra "known state" write
        # already happened there, since the init is idempotent.
        check(d.get("led_writes") == "12" and d.get("led_high") == "6",
              "led.blink drove the pin as many times as it reported", drives[0])

    # ------------------------------------------------------------- limits
    Section("limits: what each parameter accepts")

    def limits_of(run_idx, name):
        for l in caps_runs[run_idx][1:]:
            if l.startswith("OK limits=%s " % name):
                out = {}
                for tok in l.split(" ", 2)[2].split():
                    if ":" in tok:
                        k, _, v = tok.partition(":")
                        out[k] = v
                return out
        return {}

    for port in ("din", "dout", "relay", "ain", "aout", "temp", "rs232",
                 "rs485", "can"):
        lim = limits_of(0, port)
        check(lim, "%s states its limits" % port)
        # Every parameter the port advertises has to be covered: a parameter
        # with no stated limit is one the panel cannot validate and a plan can
        # get wrong offline.
        params = (shape_of(0, port).get("params") or "").split(",")
        missing = [p for p in params if p and p not in lim]
        check(not missing, "%s states a limit for every parameter it accepts" % port,
              "missing %s" % missing)

    dout_lim = limits_of(0, "dout")
    check(dout_lim.get("duty") == "0..100", "dout duty limit", str(dout_lim.get("duty")))
    check(dout_lim.get("mode") == "hold|blink", "dout mode enum", str(dout_lim.get("mode")))
    check(dout_lim.get("period", "").endswith(".."),
          "an open-ended limit is written lo..", str(dout_lim.get("period")))

    # The point of the whole line: the numbers here must be the numbers the
    # firmware's own checks use. These two are cross-checked against the
    # refusal messages the same build produced, so a limit that drifted from
    # its check fails here rather than on a production line.
    freq_lim = dout_lim.get("freq", "")
    refusal = reply_to("pt.start dout freq=99999")
    if refusal.startswith("ERR ") and ".." in freq_lim:
        lo, _, hi = freq_lim.partition("..")
        check(lo in refusal and hi in refusal,
              "the stated freq limit matches what the firmware refuses with",
              "limit %s vs %s" % (freq_lim, refusal))

    aout_lim = limits_of(0, "aout")
    refusal = reply_to("pt.start aout mv=9999")
    if refusal.startswith("ERR ") and ".." in aout_lim.get("mv", ""):
        hi = aout_lim["mv"].partition("..")[2]
        check(hi in refusal,
              "the stated mv limit matches what the firmware refuses with",
              "limit %s vs %s" % (aout_lim.get("mv"), refusal))

    can_lim = limits_of(0, "can")
    check(can_lim.get("baud") == "125000|250000|500000|1000000",
          "can's rate list is generated from the driver's table",
          str(can_lim.get("baud")))

    # ----------------------------------------------------------------- SD
    Section("sd: the two one-shots")

    sd_probes = [b for c, b in sections if c == "pt.run sd.probe"]
    check(len(sd_probes) == 2, "pt.run sd.probe answered both times",
          "%d section(s)" % len(sd_probes))
    if len(sd_probes) == 2:
        good = dict(parse_kv([l for l in sd_probes[0] if l.startswith("OK ")][0]))
        gone = dict(parse_kv([l for l in sd_probes[1] if l.startswith("OK ")][0]))
        check(good.get("detected") == "1" and good.get("ready") == "1",
              "a card that is there reads detected=1 ready=1", str(good))
        check(good.get("mib", "0") != "0", "a ready card reports a capacity", str(good.get("mib")))
        # detected and ready are separate on purpose: a card in the slot that
        # will not identify is a different fault from an empty slot, and the
        # detect pin is read before any bus traffic.
        check(gone.get("detected") == "0" and gone.get("ready") == "0",
              "an absent card reads detected=0 ready=0", str(gone))

    sd_rounds = [b for c, b in sections if c == "pt.run sd.integrity"]
    check(len(sd_rounds) == 3, "pt.run sd.integrity answered all three times",
          "%d section(s)" % len(sd_rounds))
    if len(sd_rounds) == 3:
        ok_r  = dict(parse_kv([l for l in sd_rounds[0] if l.startswith("OK ")][0]))
        bad_r = dict(parse_kv([l for l in sd_rounds[1] if l.startswith("OK ")][0]))
        no_card = dict(parse_kv([l for l in sd_rounds[2] if l.startswith("OK ")][0]))
        check(ok_r.get("identical") == "1" and ok_r.get("write_crc") == ok_r.get("read_crc"),
              "a good round reports identical=1 and matching CRCs", str(ok_r))
        # The interesting failure: it mounted, it wrote, it read - and the
        # bytes differ. Reporting only identical=0 would lose all of that.
        check(bad_r.get("identical") == "0" and bad_r.get("mounted") == "1"
              and bad_r.get("wrote") == "1" and bad_r.get("read_back") == "1",
              "a corrupt round still says how far it got", str(bad_r))
        check(bad_r.get("write_crc") != bad_r.get("read_crc"),
              "a corrupt round reports both CRCs so the difference is visible",
              str(bad_r))
        check(no_card.get("mounted") == "0" and no_card.get("fresult") == "-1",
              "with no card it never mounted, and says so", str(no_card))
        for body in sd_rounds:
            line = [l for l in body if l.startswith("OK ")][0]
            check("PASS" not in line and "FAIL" not in line,
                  "the sd.integrity reply carries measurements, not a verdict", line)

    # ---------------------------------------------------------------- CAN
    Section("can: the session")

    can_shape = shape_of(0, "can")
    check(can_shape.get("kind") == "session",
          "can is a session", str(can_shape.get("kind")))
    check(can_shape.get("loop") == "link",
          "can's loop travels over the CAN pair, so its counter is a verdict",
          str(can_shape.get("loop")))
    check(can_shape.get("targets") is None,
          "the deep CAN entries no longer ride on the session row",
          str(can_shape.get("targets")))

    can_frames = [l for _, body in sections for l in body if l.startswith("!can ")]
    check(can_frames, "can produced frames")
    if can_frames:
        f = dict(parse_kv(can_frames[0]))
        for field in ("bps", "mode", "alive", "tx", "rx_frames", "junk",
                      "replied", "tec", "rec"):
            check(field in f, "the can frame reports %s" % field, can_frames[0])
        check(f.get("bps") == "250000",
              "the frame reports the rate that was asked for", str(f.get("bps")))
        check(f.get("mode") == "loopback",
              "the frame names the mode", str(f.get("mode")))

    opened = [l for _, body in sections for l in body if l.startswith("TEST can_open=")]
    if opened:
        d = dict(parse_kv(opened[0]))
        # 1 = 250 kbit/s in the rate table, 3 = FDCAN_MODE_INTERNAL_LOOPBACK.
        check(d.get("rate") == "1" and d.get("mode") == "3",
              "baud= and mode= reached the controller", opened[0])
        check(d.get("sent") == "2",
              "one frame per tick went out in loopback", opened[0])

    listened = [l for _, body in sections for l in body
                if l.startswith("TEST can_listen_sent=")]
    if listened:
        # The one that matters. Bus monitoring exists to watch a live bus
        # without disturbing it; a "passive" mode that injected frames would be
        # worse than having no listen mode at all.
        check(listened[0].endswith("=0"),
              "listen mode transmitted nothing at all", listened[0])

    # mode=echo. Three separate things have to hold, and each one fails a
    # different way: a responder that originates would put traffic on a bus
    # somebody else is measuring; one that answers on the wrong identifier or
    # length is invisible to the node waiting for it; one that returns the
    # payload unchanged cannot be told apart from an unplugged transceiver
    # handing the sender its own frame back.
    idle = [l for _, body in sections for l in body
            if l.startswith("TEST can_echo_idle_sent=")]
    if idle:
        check(idle[0].endswith("=0"),
              "echo mode originates nothing on its own", idle[0])

    echoed = [l for _, body in sections for l in body
              if l.startswith("TEST can_echo_sent=")]
    if echoed:
        d = dict(parse_kv(echoed[0]))
        check(d.get("can_echo_sent") == "1",
              "one frame arrived and exactly one answer went out", echoed[0])
        check(d.get("id") == "123" and d.get("len") == "4",
              "the answer went back on the same identifier and length",
              echoed[0])
        check(d.get("payload") == "42",
              "the answer carries the payload incremented, not the payload",
              echoed[0])

    can_echo_frames = [dict(parse_kv(l)) for l in can_frames
                       if " mode=echo " in l]
    check(can_echo_frames, "echo mode produced frames")
    if can_echo_frames:
        check(can_echo_frames[-1].get("replied") == "1",
              "the frame reports replied=, which is the verdict in echo mode",
              str(can_echo_frames[-1]))

    for cmd, why in (("pt.set can baud=500000", "when the controller opens"),
                     ("pt.set can mode=listen", "when the controller opens"),
                     ("pt.start can baud=999", "must be one of"),
                     ("pt.start can mode=sideways", "must be normal, listen, loopback, extloop or echo"),
                     ("pt.start relay period=100", "rated 3e4 operations")):
        got = reply_to(cmd)
        check(got.startswith("ERR ") and why in got, "refused: %s" % cmd, got)

    # A controller that is not clocked looks exactly like a dead bus. Refusing
    # to start says which of the two it is, on the spot.
    got = reply_to("pt.start can")
    check(got.startswith("ERR ") and "not clocked" in got,
          "an unclocked FDCAN is refused, not reported as a silent bus", got)

    # ------------------------------------------------------------ burn-in
    # ----------------------------------------------------------------- usb
    Section("usb: the CDC session")

    usb_shape = next((d for _, d, _ in sessions if d.get("port") == "usb"), None)
    check(usb_shape is not None, "usb is a session")
    if usb_shape:
        check(usb_shape.get("loop") == "link",
              "usb closes its loop over the pipe under test", str(usb_shape.get("loop")))

    def usb_test(prefix):
        for _, body in sections:
            for l in body:
                if l.startswith(prefix):
                    return dict(parse_kv(l[len("TEST "):])), l
        return None, ""

    d, line = usb_test("TEST usb_inits=")
    if d:
        # MX_USB_DEVICE_Init enumerates the device. Calling it per start would
        # make the PC's COM port disappear and come back mid-test.
        check(d.get("usb_inits") == "1", "the USB stack was brought up once", line)
    d, line = usb_test("TEST usb_info_sent=")
    if d:
        check(d.get("inits") == "1", "still once after four more starts", line)
        check(d.get("usb_info_sent") == "0",
              "info mode put nothing on the wire", line)
    d, line = usb_test("TEST usb_sink_sent=")
    if d:
        check(d.get("usb_sink_sent") == "0",
              "sink mode transmits nothing - it only counts", line)
    d, line = usb_test("TEST usb_source_pushed=")
    if d:
        check(d.get("usb_source_pushed") == "1",
              "source mode pushes without being asked", line)

    usb_frames = [dict(parse_kv(l)) for _, body in sections for l in body
                  if l.startswith("!usb ")]
    check(usb_frames, "usb produced frames")
    if usb_frames:
        for field in ("state", "enum", "mode", "rx_bytes", "tx_bytes", "kbps", "busy"):
            check(field in usb_frames[0], "the usb frame reports %s" % field,
                  str(usb_frames[0]))

    echo = [f for f in usb_frames if f.get("mode") == "echo"]
    if len(echo) >= 8:
        check(echo[1].get("seq") == "2" and echo[1].get("miss") == "0",
              "a PC that answers closes the loop", str(echo[1]))
        # Same contract as every other loop=link port.
        check(echo[3].get("seq") == "3" and echo[3].get("miss") == "1",
              "a stale reply counts as a miss, not as a closed loop", str(echo[3]))
        # The distinction the frame exists to make: a host that stopped reading
        # is not a dead cable, and busy= is the only thing that says which.
        check(echo[4].get("busy") == "1" and echo[4].get("seq") == "3",
              "a host that stopped reading shows as busy, with seq held",
              str(echo[4]))
        check(echo[6].get("state") == "default" and echo[6].get("enum") == "1",
              "losing enumeration is reported as a state, not as silence",
              str(echo[6]))
        check(echo[7].get("enum") == "2",
              "and re-enumerating counts again - a flapping host is visible",
              str(echo[7]))

    sink = [f for f in usb_frames if f.get("mode") == "sink"]
    if sink:
        check(int(sink[0].get("rx_bytes", "0")) > 0, "sink counted what arrived",
              str(sink[0]))
        check(sink[0].get("seq") == "1" and sink[0].get("rx") == "0",
              "sink does not let payload bytes move the echo counter", str(sink[0]))

    info = [f for f in usb_frames if f.get("mode") == "info"]
    if len(info) >= 3:
        # A mode that sends nothing must not report misses for it: nobody was
        # ever given anything to answer.
        check(all(f.get("miss") == "0" for f in info),
              "info mode never counts a miss - it asked nothing of anybody",
              str([f.get("miss") for f in info]))
        check(all(f.get("tx_bytes") == "0" for f in info),
              "and moves no bytes at all", str(info[0]))

    check(reply_to("pt.echo usb 1").startswith("ERR "),
          "pt.echo is refused for usb - its reply rides the pipe, not the control port",
          reply_to("pt.echo usb 1"))

    for cmd in ("pt.start usb mode=sideways", "pt.start usb period=abc"):
        check(reply_to(cmd).startswith("ERR "), "refused: %s" % cmd, reply_to(cmd))

    # ------------------------------------------------------------ ethernet
    Section("eth: the TCP session")

    eth_shape = next((d for _, d, _ in sessions if d.get("port") == "eth"), None)
    check(eth_shape is not None, "eth is a session, not a one-shot")
    if eth_shape:
        # The whole point of DECISIONS.md 28: one row for one RJ45, with the
        # PHY probe hanging off the session rather than on a row of its own.
        check(eth_shape.get("runs") == "eth.link",
              "the eth session carries eth.link as a run target",
              str(eth_shape.get("runs")))
        check(eth_shape.get("loop") == "link",
              "eth closes its loop over the link under test", str(eth_shape.get("loop")))

    eth_rows = [d for _, d, _ in (sessions + runs)
                if d.get("port") == "eth"]
    check(len(eth_rows) == 1,
          "eth appears exactly once in caps - one card, not two",
          "%d rows" % len(eth_rows))

    def eth_test(prefix):
        for _, body in sections:
            for l in body:
                if l.startswith(prefix):
                    return dict(parse_kv(l[len("TEST "):])), l
        return None, ""

    d, line = eth_test("TEST eth_inits=")
    if d:
        # MX_LWIP_Init registers a netif and starts a DHCP client. Calling it
        # per start would register a second one.
        check(d.get("eth_inits") == "1", "lwIP was brought up exactly once", line)
        check(d.get("listening") == "1" and d.get("port") == "5000",
              "the session listened on the port it was asked for", line)

    d, line = eth_test("TEST eth_static_dhcp=")
    if d:
        check(d.get("eth_static_dhcp") == "0",
              "a static address stops the DHCP client, so no lease overwrites it",
              line)
        check(d.get("inits") == "1",
              "still one lwIP init after four more starts", line)

    d, line = eth_test("TEST eth_second_peer=")
    if d:
        # One transfer at a time: a second peer's bytes would land in the same
        # counters and the reported rate would be a mixture of two sessions.
        check(d.get("eth_second_peer") == "0", "a second peer is refused", line)

    d, line = eth_test("TEST eth_rebound=")
    if d:
        check(d.get("eth_rebound") == "5001",
              "changing port= moved the listener, it did not just record it", line)

    d, line = eth_test("TEST eth_source_pushed=")
    if d:
        check(d.get("eth_source_pushed") == "1",
              "source mode pushes without being asked", line)

    eth_frames = [l for _, body in sections for l in body if l.startswith("!eth ")]
    check(eth_frames, "eth produced frames")
    if eth_frames:
        f = dict(parse_kv(eth_frames[0]))
        for field in ("ip", "link", "conn", "mode", "port",
                      "rx_bytes", "tx_bytes", "kbps"):
            check(field in f, "the eth frame reports %s" % field, eth_frames[0])
        # A session with nobody connected must say so rather than looking idle.
        check(f.get("conn") == "0",
              "the first frame says no peer has connected yet", eth_frames[0])

    # The counter, over the frames the echo scenario produced. Same contract as
    # every other loop=link port: no reply leaves seq where it was.
    echo_frames = [dict(parse_kv(l)) for l in eth_frames
                   if dict(parse_kv(l)).get("mode") == "echo"]
    if len(echo_frames) >= 6:
        check(echo_frames[1].get("seq") == "1" and echo_frames[1].get("miss") == "1",
              "with no peer the counter stalls and counts a miss",
              str(echo_frames[1]))
        check(echo_frames[3].get("seq") == "2" and echo_frames[3].get("miss") == "0",
              "a peer that answers closes the loop", str(echo_frames[3]))
        # A reply to a frame that has passed must not close anything.
        check(echo_frames[5].get("seq") == "3" and echo_frames[5].get("miss") == "1",
              "a stale reply counts as a miss, not as a closed loop",
              str(echo_frames[5]))

    sink = [dict(parse_kv(l)) for l in eth_frames
            if dict(parse_kv(l)).get("mode") == "sink"]
    if sink:
        check(int(sink[0].get("rx_bytes", "0")) > 0,
              "sink counted what arrived", str(sink[0]))
        # sink does not parse the payload, so junk must not move the counter.
        check(sink[0].get("seq") == "1" and sink[0].get("rx") == "0",
              "sink does not let payload bytes move the echo counter",
              str(sink[0]))

    check(reply_to("pt.echo eth 1").startswith("ERR "),
          "pt.echo is refused for eth - its reply rides the link, not the control port",
          reply_to("pt.echo eth 1"))

    for cmd in ("pt.start eth mode=sideways", "pt.start eth port=0",
                "pt.start eth port=70000", "pt.start eth ip=192.168.1"):
        check(reply_to(cmd).startswith("ERR "), "refused: %s" % cmd, reply_to(cmd))

    Section("the deadman: pt.hold")

    # The PC owns the clock for a timed run, so the board has to survive the PC
    # going away mid-run. Nothing here is the board judging a test - it judges
    # whether anyone is still listening.
    hold_head = [l for _, body in sections for l in body
                 if l.startswith("OK porttool=")]
    check(hold_head and "hold=1" in hold_head[0],
          "caps advertises pt.hold, so the PC never has to probe for it",
          hold_head[0] if hold_head else "")
    check(hold_head and "led=1" in hold_head[0],
          "caps advertises pt.led", hold_head[0] if hold_head else "")

    check(reply_to("pt.hold") .startswith("OK hold="),
          "a bare pt.hold answers rather than arming", reply_to("pt.hold"))
    for cmd in ("pt.hold abc", "pt.hold 999999999"):
        check(reply_to(cmd).startswith("ERR "), "refused: %s" % cmd, reply_to(cmd))

    live = [l for _, body in sections for l in body
            if l.startswith("TEST hold_live_driving=")]
    renewed = [l for _, body in sections for l in body
               if l.startswith("TEST hold_renewed_driving=")]
    expired = [l for _, body in sections for l in body
               if l.startswith("TEST hold_expired_driving=")]
    disarmed = [l for _, body in sections for l in body
                if l.startswith("TEST hold_disarmed_dout_duty_any=")]

    if live:
        d = dict(parse_kv(live[0]))
        check(d.get("dout_duty_any") == "1" and d.get("hold_live_driving") == "1",
              "an armed hold does not disturb what is running", live[0])
    if renewed:
        d = dict(parse_kv(renewed[0]))
        check(d.get("dout_duty_any") == "1" and d.get("hold_renewed_driving") == "1",
              "renewing before the deadline keeps the outputs driven", renewed[0])
    if expired:
        d = dict(parse_kv(expired[0]))
        # The whole point: outputs released without anybody on the PC side.
        check(d.get("dout_duty_any") == "0" and d.get("hold_expired_driving") == "0",
              "a lapsed hold released every output", expired[0])
        check(d.get("led_high") == "1",
              "a lapsed hold lit the indicator itself - the PC is gone, so "
              "nobody else would", expired[0])

    frames = [l for _, body in sections for l in body if l.startswith("!hold ")]
    check(frames, "the board says out loud that the hold lapsed")
    if frames:
        f = dict(parse_kv(frames[-1]))
        check(f.get("expired") == "1", "the hold frame reports expiry", frames[-1])

    stopped = [l for _, body in sections for l in body
               if l.startswith("OK running=")]
    check(any(l == "OK running=none" for l in stopped),
          "every session is stopped after the hold lapsed")

    if disarmed:
        # pt.hold 0 means "stop watching the clock", not "stop the test".
        check(disarmed[0].endswith("=1"),
              "disarming the hold left the running session alone", disarmed[0])

    Section("the indicator: pt.led")

    on = [l for _, body in sections for l in body if l.startswith("TEST led_fault_on=")]
    off = [l for _, body in sections for l in body if l.startswith("TEST led_fault_off=")]
    if on:
        d = dict(parse_kv(on[0]))
        check(d.get("led_fault_on") == "1", "pt.led fault=1 lit the pin", on[0])
        check(d.get("writes") != "0", "pt.led actually drove the pin", on[0])
    if off:
        d = dict(parse_kv(off[0]))
        check(d.get("led_fault_off") == "0" and d.get("writes") != "0",
              "pt.led fault=0 drove the pin without driving it high", off[0])

    for cmd in ("pt.led fault=2", "pt.led"):
        check(reply_to(cmd).startswith("ERR "), "refused: %s" % cmd, reply_to(cmd))

    # A run must not disturb what was already going.
    probes = [l for _, body in sections for l in body
              if l.startswith("TEST sdram_probe_count=")]
    if probes:
        check(probes[-1].endswith("=2"),
              "the probe ran once per pt.run and not otherwise", probes[-1])

    Section("result")
    print("  transcript written to %s (%d lines)" % (GOLDEN.name, len(board_lines)))
    if failures:
        Fail("%d check(s) failed" % len(failures))
        return 1
    Ok("firmware side: all checks passed")

    # The other half of the same case. The Go parser is only worth anything if
    # it reads the bytes the firmware just produced, so the two run together --
    # split apart, they would drift and each would still look green.
    return run_go_side(cc)


def run_go_side(cc):
    """Runs the Go tests over the transcript that was just written.

    With the race detector when a C compiler is around, because it needs cgo.
    That is not decoration: ptboard reads the serial line on its own goroutine
    while commands are sent from another and a browser view subscribes from a
    third, and the first race run found a real one -- a subscriber channel
    closed while the reader was still delivering to it, which in production is
    a panic the moment somebody closes a browser tab mid-session.
    """
    import os
    import shutil
    Section("Go side against that transcript")
    if not shutil.which("go"):
        Warn("SKIP  go is not on PATH, so internal/ptproto and ptboard were not exercised")
        return 0

    argv = ["go", "test", "-count=1", "./TestCase/host/porttool_caps/"]
    env = dict(os.environ)
    if cc and Path(cc).exists():
        argv.insert(2, "-race")
        env["CGO_ENABLED"] = "1"
        env["CC"] = str(cc)
        print("  with -race (CC=%s)" % cc)
    else:
        Warn("  no C compiler: running without -race, so concurrency is not covered")

    proc = subprocess.run(argv, cwd=str(HERE.parent.parent.parent), env=env,
                          stdout=subprocess.PIPE, stderr=subprocess.STDOUT)
    sys.stdout.flush()
    sys.stdout.buffer.write(proc.stdout)
    sys.stdout.buffer.flush()
    if proc.returncode != 0:
        Fail("the Go caps parser disagrees with what the firmware printed")
        return 1
    Ok("Go side: parser agrees with the firmware")
    return 0


if __name__ == "__main__":
    sys.exit(main())
