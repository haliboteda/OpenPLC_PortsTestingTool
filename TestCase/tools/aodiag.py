"""TEMPORARY: steps a person through the AO noise experiments (pt.aodiag).

Remove together with pt.aodiag once the noise source is found. Experiments,
probes and how to read the results:
$PROD/maps/fixture-test-stages/issues/STG-41-where-does-the-ao-noise-come-from.md

    python tools/aodiag.py --port COM5          # real board, control port
    python tools/aodiag.py --port sim --auto    # simulated board, no prompts

For each experiment: says what is about to happen (printed and spoken), waits
for Enter, runs it, then asks for the Vpp / frequency seen at PA4, TP3 and the
AO terminal (blank allowed). Writes Output/aodiag/<timestamp>.csv and
<timestamp>-summary.txt.

Exit 0 = every experiment ran, 1 = the board refused or timed out, 2 = could
not open the port.
"""

import argparse
import csv
import queue
import subprocess
import sys
import threading
import time
from datetime import datetime
from pathlib import Path

HERE = Path(__file__).resolve().parent
sys.path.insert(0, str(HERE))

from common import EXE, IS_WIN, PLATFORM, Section, Ok, Warn, Fail  # noqa: E402

REPO = HERE.parents[1]
SIMBOARD = REPO / "Output" / "bin" / ({"windows": "windows", "linux": "linux"}.get(PLATFORM) or "darwin") / ("porttool_simboard" + EXE)
OUT_DIR = REPO / "Output" / "aodiag"
TICKET = "$PROD/maps/fixture-test-stages/issues/STG-41-where-does-the-ao-noise-come-from.md"

# (exp, scale or None, English cue, Chinese cue)
RUNS = [
    ("E0", None, "Baseline: DAC static, firmware idle. Note the noise.",
     "基线：DAC 固定输出，固件空闲。记下噪声。"),
    ("E1", None, "Quiet window: all interrupts off, CPU asleep. Does the noise go away?",
     "安静窗口：中断全关、CPU 睡眠。噪声还在吗？"),
    ("E2", None, "Quiet window, DAC buffer off. PA4 level drops; does the noise change?",
     "安静窗口，DAC 缓冲关。PA4 电平会掉；噪声变不变？"),
    ("E3", 0, "Quiet window, VREFBUF 2.5 V.", "安静窗口，基准 2.5 伏。"),
    ("E3", 1, "Quiet window, VREFBUF 2.048 V. Does the noise follow the reference?",
     "安静窗口，基准 2.048 伏。噪声跟着基准变吗？"),
    ("E3", 2, "Quiet window, VREFBUF 1.8 V.", "安静窗口，基准 1.8 伏。"),
    ("E3", 3, "Quiet window, VREFBUF 1.5 V.", "安静窗口，基准 1.5 伏。"),
    ("E4", None, "ADC converting AI1 every 1 ms. Does a 1 kHz component appear?",
     "ADC 每毫秒转换一次 AI1。出现 1 千赫的成分吗？"),
    ("E5", None, "Control UART sends a line every 10 ms. Does a 100 Hz component appear?",
     "控制串口每 10 毫秒发一行。出现 100 赫的成分吗？"),
    ("E6", None, "DAC channel off, PA4 on the 1k pull-down. PA4 should be near 0 V; any noise left?",
     "DAC 通道关，PA4 只剩下拉。PA4 应接近 0 伏；还有噪声吗？"),
]


# ---------------------------------------------------------------- speech
def speak(text, enabled):
    """Windows SAPI (Chinese voice if installed) or macOS say; silent otherwise."""
    if not enabled:
        return
    try:
        if IS_WIN:
            ps = ("Add-Type -AssemblyName System.Speech;"
                  "$s=New-Object System.Speech.Synthesis.SpeechSynthesizer;"
                  "try{$s.SelectVoice('Microsoft Huihui Desktop')}catch{};"
                  "$s.Speak($env:AODIAG_SAY)")
            env = dict(__import__("os").environ, AODIAG_SAY=text)
            subprocess.run(["powershell", "-NoProfile", "-Command", ps], env=env,
                           stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, timeout=30)
        elif PLATFORM == "macos":
            subprocess.run(["say", text], stdout=subprocess.DEVNULL,
                           stderr=subprocess.DEVNULL, timeout=30)
    except Exception:
        pass


# ---------------------------------------------------------------- links
class SimLink:
    def __init__(self):
        if not SIMBOARD.exists():
            raise OSError("%s not built - run: tools/build.sh sim" % SIMBOARD)
        self.p = subprocess.Popen([str(SIMBOARD)], stdin=subprocess.PIPE,
                                  stdout=subprocess.PIPE, stderr=subprocess.DEVNULL)
        self.q = queue.Queue()
        threading.Thread(target=self._read, daemon=True).start()

    def _read(self):
        for raw in self.p.stdout:
            self.q.put(raw.decode("utf-8", "replace").rstrip("\r\n"))

    def send(self, line):
        self.p.stdin.write((line + "\n").encode())
        self.p.stdin.flush()

    def recv(self, timeout):
        try:
            return self.q.get(timeout=timeout)
        except queue.Empty:
            return None

    def close(self):
        self.p.kill()


class SerialLink:
    def __init__(self, port):
        import serial
        self.s = serial.Serial(port, 115200, timeout=0.2)
        self.buf = b""

    def send(self, line):
        self.s.write((line + "\r\n").encode())

    def recv(self, timeout):
        end = time.time() + timeout
        while True:
            if b"\n" in self.buf:
                raw, _, self.buf = self.buf.partition(b"\n")
                return raw.decode("utf-8", "replace").rstrip("\r")
            if time.time() > end:
                return None
            self.buf += self.s.read(256)

    def close(self):
        self.s.close()


def wait_for(link, prefixes, timeout):
    """Next line starting with one of prefixes; frames and log lines skipped."""
    end = time.time() + timeout
    while True:
        left = end - time.time()
        if left <= 0:
            return None
        line = link.recv(left)
        if line is None:
            return None
        if line.startswith(prefixes):
            return line


def command(link, line, timeout=5.0):
    link.send(line)
    return wait_for(link, ("OK", "ERR"), timeout)


# ---------------------------------------------------------------- run
def ask(prompt, auto):
    if auto:
        return ""
    try:
        return input(prompt).strip()
    except EOFError:
        return ""


def main():
    ap = argparse.ArgumentParser(description=__doc__.split("\n")[0])
    ap.add_argument("--port", required=True, help="control COM port, or sim")
    ap.add_argument("--ch", type=int, default=1, choices=(1, 2), help="AO channel")
    ap.add_argument("--mv", type=int, default=1000, help="DAC millivolts")
    ap.add_argument("--secs", type=int, default=10, help="seconds per experiment")
    ap.add_argument("--only", default="", help="comma list, e.g. E1,E3")
    ap.add_argument("--auto", action="store_true", help="no Enter, no readings, no voice")
    ap.add_argument("--no-voice", action="store_true")
    a = ap.parse_args()
    voice = not (a.auto or a.no_voice)

    pin = "PA4" if a.ch == 1 else "PA5"
    term = "D14" if a.ch == 1 else "D15"
    only = {x.strip().upper() for x in a.only.split(",") if x.strip()}
    runs = [r for r in RUNS if not only or r[0] in only]

    Section("open %s" % a.port)
    try:
        link = SimLink() if a.port.lower() == "sim" else SerialLink(a.port)
    except Exception as e:
        Fail("could not open %s: %s" % (a.port, e))
        return 2

    rows = []
    failed = 0
    try:
        for cmd in ("pt.hold 0", "pt.stop all"):
            r = command(link, cmd)
            print("  %s -> %s" % (cmd, r))
            if r is None:
                Fail("no reply - is this the control port?")
                return 2

        print("Probe: %s (DAC pin), TP3 (VREF+), AO terminal %s; scope AC-coupled." % (pin, term))
        print("探点：%s（DAC 脚）、TP3（VREF+）、AO 端子 %s；示波器 AC 耦合。" % (pin, term))
        print("How to read results: %s" % TICKET)

        for i, (exp, scale, en, zh) in enumerate(runs, 1):
            label = exp if scale is None else "%s scale=%d" % (exp, scale)
            Section("%d/%d  %s" % (i, len(runs), label))
            print("  " + en)
            print("  " + zh)
            speak("下一个实验，%s。%s 准备好后按回车。" % (exp, zh), voice)
            ask("  Press Enter to start / 按回车开始 ", a.auto)

            line = "pt.aodiag exp=%s ch=%d mv=%d secs=%d" % (exp, a.ch, a.mv, a.secs)
            if scale is not None:
                line += " scale=%d" % scale
            start = command(link, line)
            print("  %s -> %s" % (line, start))
            if start is None or not start.startswith("OK aodiag") or "starting" not in start:
                Fail("  refused or no reply")
                failed += 1
                rows.append(dict(run=label, exp=exp, scale="" if scale is None else scale,
                                 ch=a.ch, mv=a.mv, secs=a.secs, board=start or "timeout"))
                continue
            speak("现在开始，看示波器。", voice)
            done = wait_for(link, ("OK aodiag done", "ERR aodiag"), a.secs + 15)
            print("  -> %s" % done)
            speak("结束。", voice)
            if done is None or not done.startswith("OK"):
                Fail("  did not finish cleanly")
                failed += 1

            row = dict(run=label, exp=exp, scale="" if scale is None else scale,
                       ch=a.ch, mv=a.mv, secs=a.secs, board=done or "timeout")
            for where in (pin, "TP3", term):
                row["%s_vpp" % where] = ask("  %s Vpp (mV, blank = skip): " % where, a.auto)
                row["%s_freq" % where] = ask("  %s frequency (Hz, blank = skip): " % where, a.auto)
            row["note"] = ask("  note / 备注: ", a.auto)
            rows.append(row)
    finally:
        link.close()

    OUT_DIR.mkdir(parents=True, exist_ok=True)
    stamp = datetime.now().strftime("%Y%m%d-%H%M%S")
    csv_path = OUT_DIR / ("%s.csv" % stamp)
    cols = ["run", "exp", "scale", "ch", "mv", "secs", "board"]
    for where in (pin, "TP3", term):
        cols += ["%s_vpp" % where, "%s_freq" % where]
    cols.append("note")
    with open(csv_path, "w", newline="", encoding="utf-8") as f:
        w = csv.DictWriter(f, fieldnames=cols, extrasaction="ignore")
        w.writeheader()
        for r in rows:
            w.writerow(r)

    summary = OUT_DIR / ("%s-summary.txt" % stamp)
    lines = ["AO noise experiments %s  port=%s ch=%d mv=%d secs=%d" %
             (stamp, a.port, a.ch, a.mv, a.secs), "how to read: " + TICKET, ""]
    lines.append("%-12s %-10s %-10s %-10s %s" % ("run", pin + " Vpp", "TP3 Vpp", term + " Vpp", "board"))
    for r in rows:
        lines.append("%-12s %-10s %-10s %-10s %s" % (
            r["run"], r.get("%s_vpp" % pin, ""), r.get("TP3_vpp", ""),
            r.get("%s_vpp" % term, ""), r["board"]))
    summary.write_text("\n".join(lines) + "\n", encoding="utf-8")

    Section("result")
    print("\n".join(lines))
    print("")
    print("  csv      %s" % csv_path)
    print("  summary  %s" % summary)
    if failed:
        Fail("%d experiment(s) did not run cleanly" % failed)
        return 1
    Ok("all %d experiment(s) ran" % len(rows))
    if a.port.lower() == "sim":
        Warn("sim: the command path only - no real masking, sleep, DAC or VREFBUF")
    return 0


if __name__ == "__main__":
    sys.exit(main())
