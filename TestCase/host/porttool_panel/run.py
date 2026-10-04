"""Clicks through the port tool panel in a real browser. Case T4-02.

    python run.py --port COM12          the board's RS232 control port
    python run.py --port COM12 --show   watch it happen in a visible window
    python run.py --wsl Debian --port /dev/ttyUSB1
                                        case T4-03: the Linux build, run in WSL

Why a browser and not the HTTP API: case T4-01 already covers the protocol and the
parser, and the panel's own API is covered by the Go tests. What neither of them
can see is whether the page a person actually looks at renders the right thing
and whether its buttons do what they say - and that is where the bugs were
found on 2026-09-08 (a lost reply and a corrupted command, both invisible to
the parser tests).

Needs a board: the panel's whole job is talking to one, so a panel test without
a board would only exercise the empty state. It never writes flash and never
drives an output that needs wiring; it starts and stops sessions.

Requires playwright and a Chrome install:
    python -m pip install playwright
The system Chrome is used (channel="chrome"), so no browser is downloaded.

Exit 0 = every check held, 1 = a check failed, 2 = could not run at all.
"""

import argparse
import os
import re
import subprocess
import sys
import tempfile
import time

# Progress has to be visible while this runs, not only when it exits: piped
# stdout is block-buffered by default, and a stuck run then looks identical to
# a slow one.
try:
    sys.stdout.reconfigure(line_buffering=True)
except AttributeError:
    pass
from pathlib import Path

HERE = Path(__file__).resolve().parent
REPO = HERE.parents[2]
sys.path.insert(0, str(HERE.parent.parent / "tools"))

from common import EXE, cfg, Section, Ok, Fail, Warn  # noqa: E402

failures = []


def check(cond, what, detail=""):
    if cond:
        Ok("PASS  %s" % what)
    else:
        Fail("FAIL  %s%s" % (what, ("  --  " + detail) if detail else ""))
        failures.append(what)
    return cond


# Set by --wsl: the distribution the Linux build runs in. The browser stays on
# Windows and reaches the panel through WSL's localhost forwarding.
WSL = ""


def panel_exe():
    if WSL:
        p = REPO / "Output" / "linux" / "PortTool"
    else:
        p = REPO / "Output" / "windows" / ("PortTool" + EXE)
    if not p.exists():
        Fail("no PortTool at %s - run compile_tool.sh" % p)
        sys.exit(2)
    return p


def port_map_path(exe):
    return exe.parent / "porttool_ports.json"


def start_panel(exe):
    """Starts the panel and returns (process, base url, log path).

    The panel picks its own port, so the address is read from its output rather
    than guessed.

    Its output goes to a FILE, not a pipe. With a pipe, this script reads the
    address line and then stops reading, the panel keeps writing, the pipe
    fills, and the panel blocks - after which it does not even see terminate().
    That deadlock hung a completed run on 2026-09-08: every check had passed
    and the process would not exit. A file also leaves the panel's own log
    behind when something needs explaining.
    """
    log = Path(tempfile.gettempdir()) / "porttool_panel_test.log"
    fh = open(log, "w", encoding="utf-8", errors="replace")
    cmd = [str(exe), "--no-browser"]
    if WSL:
        cmd = ["wsl", "-d", WSL, "--cd", str(exe.parent), "./" + exe.name, "--no-browser"]
    proc = subprocess.Popen(cmd, cwd=str(exe.parent),
                            stdout=fh, stderr=subprocess.STDOUT)
    deadline = time.time() + 20
    while time.time() < deadline:
        if proc.poll() is not None:
            Fail("the panel exited immediately - see %s" % log)
            sys.exit(2)
        try:
            text = log.read_text(encoding="utf-8", errors="replace")
        except OSError:
            text = ""
        m = re.search(r"http://(127\.0\.0\.1:\d+)", text)
        if m:
            return proc, "http://" + m.group(1), log
        time.sleep(0.2)
    Fail("the panel never printed its address - see %s" % log)
    proc.kill()
    sys.exit(2)


def pids_of(names):
    """The process ids currently running under any of `names`."""
    out = set()
    for name in names:
        try:
            r = subprocess.run(["tasklist", "/FI", "IMAGENAME eq " + name,
                                "/FO", "CSV", "/NH"],
                               capture_output=True, text=True, errors="replace")
        except OSError:
            continue
        for line in (r.stdout or "").splitlines():
            parts = [p.strip('"') for p in line.split('","')]
            if len(parts) >= 2 and parts[1].isdigit():
                out.add(parts[1])
    return out


def kill_new(names, before):
    """Kills only the processes this run started.

    browser.close() hangs with channel="chrome" on this machine - confirmed
    2026-09-08 by instrumenting the teardown: leaving the page returns, closing
    the browser never does. Rather than wait on an SDK that will not answer,
    the run notes which chrome and driver processes existed beforehand and ends
    exactly the ones it added. Killing by image name would take somebody's
    other browser with it.
    """
    for pid in sorted(pids_of(names) - before):
        subprocess.run(["taskkill", "/F", "/T", "/PID", pid],
                       capture_output=True)


BROWSER_IMAGES = ("chrome.exe", "node.exe")


def panel_source():
    """The panel page as it is embedded in the executable."""
    return (REPO / "internal" / "ptpanel" / "web" / "index.html")


def node_exe():
    """Playwright ships a node; there is no need for one on PATH."""
    try:
        import playwright
    except ImportError:
        return None
    cand = Path(playwright.__file__).parent / "driver" / ("node" + EXE)
    return cand if cand.exists() else None


def check_page_parses():
    """Fails on a broken page before anything is launched.

    A syntax error in the page does not look like one from the outside: the
    panel serves it happily, the browser stops at the first bad token, nothing
    renders, and every check below times out on a selector. That is exactly how
    a stray line cost a full run on 2026-09-08 - the reported failure was
    "waiting for #portlist .p", nowhere near the mistake.
    """
    src = panel_source()
    if not src.exists():
        Fail("no panel page at %s" % src)
        failures.append("panel page missing")
        return
    js = "\n".join(re.findall(r"<script>(.*?)</script>", src.read_text(encoding="utf-8"), re.S))
    node = node_exe()
    if node is None:
        Warn("no node to parse the page with - skipping the syntax check")
        return
    tmp = Path(tempfile.gettempdir()) / "porttool_panel_page.js"
    tmp.write_text(js, encoding="utf-8")
    r = subprocess.run([str(node), "--check", str(tmp)],
                       capture_output=True, text=True, errors="replace")
    check(r.returncode == 0, "the panel page parses",
          (r.stderr or r.stdout or "").strip()[:400])


def watch_for_page_errors(page):
    """Any error the page throws fails the run, wherever it happened.

    Without this a runtime error is silent: the page half-renders and the
    checks below report whatever is left, which reads as a product bug in the
    wrong place.
    """
    def on_error(err):
        Fail("the page threw: %s" % str(err).splitlines()[0])
        failures.append("page error")

    def on_console(msg):
        if msg.type != "error":
            return
        # A failed request is reported without its URL in the text, so the
        # location is what says which one. Chrome asks for /favicon.ico on
        # every page whether one is offered or not.
        where = (msg.location or {}).get("url", "")
        if "favicon" in where:
            return
        Fail("the page logged an error: %s  (%s)" % (msg.text[:200], where))
        failures.append("console error")

    page.on("pageerror", on_error)
    page.on("console", on_console)


def stop_panel(proc):
    """Ends the panel, and does not wait forever for it to agree."""
    if WSL:
        # Ending wsl.exe does not end the program it started inside WSL.
        subprocess.run(["wsl", "-d", WSL, "-e", "pkill", "-x", "PortTool"],
                       capture_output=True)
    proc.terminate()
    try:
        proc.wait(timeout=5)
        return
    except subprocess.TimeoutExpired:
        pass
    proc.kill()
    try:
        proc.wait(timeout=5)
    except subprocess.TimeoutExpired:
        Warn("the panel would not exit; leaving it to the OS")


def main():
    ap = argparse.ArgumentParser(description=__doc__)
    ap.add_argument("--port", required=True, help="the board's RS232 control port")
    ap.add_argument("--show", action="store_true", help="visible browser window")
    ap.add_argument("--wsl", metavar="DISTRO", help="run the Linux build in this WSL distribution (T4-03)")
    args = ap.parse_args()
    global WSL
    WSL = args.wsl or ""

    try:
        from playwright.sync_api import sync_playwright
    except ImportError:
        Fail("playwright is not installed - python -m pip install playwright")
        return 2

    exe = panel_exe()

    Section("the page itself")
    check_page_parses()

    # The first half of this test is the first-run state - nothing remembered,
    # so nothing preselected and connect refused. The mapping the panel writes
    # is checked at the end instead, which covers both without needing a second
    # panel process.
    saved = port_map_path(exe)
    if saved.exists():
        saved.unlink()

    browsers_before = pids_of(BROWSER_IMAGES)

    Section("panel")
    proc, base, log = start_panel(exe)
    print("  %s   control port %s" % (base, args.port))
    print("  panel log %s" % log)

    try:
        with sync_playwright() as pw:
            browser = pw.chromium.launch(channel="chrome", headless=not args.show)
            # accept_downloads, or the log export check below has nothing to
            # catch: Chrome would just drop the file on the floor.
            page = browser.new_page(accept_downloads=True)
            page.set_default_timeout(10000)
            watch_for_page_errors(page)
            page.goto(base)
            run_checks(page, args.port)

            check_remembered(saved, args.port)

            # Leave the page before closing the browser. It holds an SSE
            # connection to the panel, and closing the browser with that open
            # left playwright's driver waiting forever on 2026-09-08 - every
            # check had passed and the run would not end.
            # Not browser.close(): see kill_new. Leaving the page first so
            # the SSE connection to the panel is dropped politely.
            page.goto("about:blank")
    finally:
        stop_panel(proc)
        kill_new(BROWSER_IMAGES, browsers_before)

    Section("result")
    if failures:
        Fail("%d check(s) failed" % len(failures))
        code = 1
    else:
        Ok("every check held")
        code = 0

    # The work is done and reported. Playwright's driver can outlive this
    # process, and waiting politely for it is how a finished run turns into a
    # hung one - so say the answer, then go.
    sys.stdout.flush()
    os._exit(code)


def check_warns_before_you_press(page):
    """The panel says what is wrong while it is being typed, not after.

    Every one of these was somebody finding out too late: a number went down
    the wire and the board refused it, or a test ran to completion and the
    verdict was about a jumper nobody had mentioned. Asked for 2026-09-14 -
    "面板让你填一个板子根本不收的数字，也让你开始一个注定过不了的测试".
    """
    Section("按下去之前就说")

    page.locator('.tab[data-tab="manual"]').click()
    page.wait_for_selector(".prow")

    def open_port(name):
        page.locator('.prow[data-port="%s"]' % name).first.click()
        page.wait_for_timeout(400)
        return page.locator('.card[data-port="%s"]' % name)

    # *** 范围出自板子报的 limits，不是写死的。*** 所以这里不断言"最小 1000"，
    # 只断言"填了一个板子收不下的数，当场有人告诉你"。固件改了上下限，用例不用改。
    card = open_port("relay")
    box = card.locator('label[data-param="period"] input')
    lo = box.get_attribute("min")
    if not check(lo is not None, "继电器周期这一格带着板子报的下限"):
        return
    box.fill(str(int(lo) - 500))
    page.wait_for_timeout(300)
    tip = card.locator(".rangetip:visible")
    check(tip.count() > 0, "填一个小于下限的数，当场就说",
          " ".join(card.inner_text().split())[:120])
    if tip.count():
        check(lo in tip.first.inner_text(), "而且说了板子到底收多少", tip.first.inner_text())
    check("bad" in (box.get_attribute("class") or ""), "那一格自己也标出来了")

    # 改回一个板子收得下的值，提示要走干净 —— 留着的话下一个人会以为还没改对。
    box.fill(lo)
    page.wait_for_timeout(300)
    check(card.locator(".rangetip:visible").count() == 0, "改回收得下的值，提示就没了")

    # 触点是消耗品，可是只有真要跑几小时的时候才值得说。
    check(card.locator(".headsup").count() == 0, "单次跑不提触点寿命 —— 跑一轮不磨它")
    card.locator('input[data-mode="loop"]').check()
    page.wait_for_timeout(400)
    hu = card.locator(".headsup")
    if check(hu.count() > 0, "选了持续，触点寿命当场说"):
        check("触点" in hu.first.inner_text(), "说的是触点这件事", hu.first.inner_text()[:80])
    card.locator('input[data-mode="once"]').check()
    page.wait_for_timeout(400)
    check(card.locator(".headsup").count() == 0, "切回单次，那句话跟着走")

    # 每路一格的参数也要逐格说 —— 八格里有一格超了，"有一格超了"等于没说。
    card = open_port("dout")
    f = card.locator('.chvals[data-param="freq"] input[data-ch="2"]')
    hi = f.get_attribute("max")
    if check(hi is not None, "DO 频率这一格带着板子报的上限"):
        f.fill(str(int(hi) + 1000))
        page.wait_for_timeout(300)
        tips = card.locator(".rangetip:visible")
        check(tips.count() == 1, "八格里超了一格，就只有那一格在说话",
              "%d 条提示" % tips.count())
        f.fill(hi)
        page.wait_for_timeout(300)

    # 软件读不回来的事：跳线焊没焊、半双工听不见自己、板子这一侧到底管什么。
    # 它们决定结论怎么读，所以必须在按之前说。
    #
    # ⚠️ **不许在这里要求面板提某一种夹具**（DECISIONS 43）。DO→DI 那根八芯线是
    # 一种接法不是必然做法 —— 断言写成「要提八芯线」，就等于用例把那个夹具又钉
    # 回去了。这里只断言板子这一侧的事实。
    for name, want, why in (
            ("dout", "读不回来", "端子上真出没出 24 V，板上没有回读"),
            ("din",  "只读不驱动", "它等的是外面给到端子上的 24 V"),
            ("ain",  "JP5",    "跳线没焊时读数会趴在 0，那不是 AI 坏了"),
            ("aout", "JP3",    "端子上出来几伏几毫安取决于跳线"),
            ("rs485", "半双工", "板子说话的时候听不见自己，所以非要对端")):
        card = open_port(name)
        hu = card.locator(".headsup")
        said = hu.first.inner_text() if hu.count() else ""
        check(want in said, "%s 按之前就说「%s」 —— %s" % (name, want, why),
              " ".join(said.split())[:110] or "（这一块是空的）")


def check_channel_labels(page):
    """Every per-channel control says which channel AND which terminal.

    The two numberings are offset: dout's channel 3 is terminal A05. A box
    labelled only A03 is the first channel, and somebody who wants DO3 will
    reach for it - which is exactly what happened on 2026-09-10.

    And the channel checkboxes have to be on the card next to the values. They
    were only in the left tree, which is a different panel with a different
    numbering in front of it.
    """
    Section("channel labels and the channel picker")

    page.locator('.tab[data-tab="manual"]').click()
    page.wait_for_selector(".prow")
    rows = page.locator(".prow")
    picked = False
    for i in range(rows.count()):
        if rows.nth(i).get_attribute("data-port") == "dout":
            rows.nth(i).click()
            page.wait_for_timeout(300)
            picked = True
            break
    if not check(picked, "dout is in the port list"):
        return

    card = page.locator('.card[data-port="dout"]')
    if not check(card.count() > 0, "the dout card is up"):
        return
    text = card.first.inner_text()

    # The name the engineer uses, not the terminal number (user 2026-09-11).
    # The terminal range still rides on the card header for whoever wires it.
    flat = " ".join(text.split())
    check("DO3" in flat and "dout3" not in flat,
          "a control names the channel the way the engineer does",
          str([l for l in text.splitlines() if "DO3" in l][:2]))
    check("端子排 A" in flat and "A03-A10" in flat,
          "and the card header still says where to wire it",
          str([l for l in text.splitlines() if "A03-A10" in l][:2]))

    check("测哪几路" in text,
          "the channel picker is on the card, not only in the tree")

    # A parameter with no unit and no range is one the reader has to guess at.
    check("占空比" in text and "Hz" in text,
          "duty and freq say what they are, in units", text[:400])
    check("0..100" in text,
          "and the range comes from the board's own limits line", text[:400])

    # Ticking a box on the card has to move the same state the tree shows.
    boxes = card.first.locator(".chvals input[type=checkbox]")
    if check(boxes.count() >= 8, "there is one checkbox per channel",
             "%d boxes" % boxes.count()):
        # Scoped to the duty grid, not the whole card: the picker lists every
        # channel whether it is selected or not, so its own labels would make
        # this assertion pass for the wrong reason.
        def duty_labels():
            box = page.locator('.card[data-port="dout"]').first \
                      .locator('.chvals[data-param="duty"]').first
            return " ".join(box.inner_text().split())

        check("DO3" in duty_labels(),
              "the duty grid has a box for channel 3 to start with", duty_labels())

        boxes.nth(2).uncheck()
        page.wait_for_timeout(400)
        check("DO3" not in duty_labels(),
              "unticking a channel on the card drops its value box too",
              duty_labels())

        # The tree is the other place the same state is shown; both read the
        # same picked set, so a tick in one has to be a tick in the other.
        tree = page.locator("#treebody").inner_text()
        check('data-port="dout"' in page.locator("#treebody").inner_html(),
              "the tree is still there to agree with")

        page.locator('.card[data-port="dout"]').first \
            .locator(".chvals input[type=checkbox]").nth(2).check()
        page.wait_for_timeout(400)
        check("DO3" in duty_labels(),
              "and ticking it back brings the value box in again", duty_labels())


def pick_port(page, name):
    """Clicks the row for one port. The rows read in Chinese, so the port is
    identified by its data attribute rather than by what is printed."""
    page.locator('.tab[data-tab="manual"]').click()
    page.wait_for_selector(".prow")
    rows = page.locator(".prow")
    for i in range(rows.count()):
        if rows.nth(i).get_attribute("data-port") == name:
            rows.nth(i).click()
            page.wait_for_timeout(300)
            return True
    return False


def check_off_plan_params(page):
    """Changing a parameter has to withdraw the verdict, not fail the board.

    The criteria come from the plan and were written for the plan's parameters.
    Pick one channel out of eight and halve its duty, and ch1/ch8 are no longer
    in the frame at all - so every criterion misses and a healthy board reads
    as failed. That is what a user hit on 2026-09-11: DO3 at 50 %, one press,
    1.4 s, 失败. Nothing in this suite had ever pressed the button with
    anything other than the plan's own parameters, which is why it survived.
    """
    Section("changing a parameter withdraws the verdict")

    if not check(pick_port(page, "dout"), "dout is in the port list"):
        return
    card = page.locator('.card[data-port="dout"]').first
    row0 = page.locator(".startrow").first.inner_text()
    check("单次" in row0 and "持续" in row0,
          "the card offers 单次 / 持续 and says what each is for", " ".join(row0.split())[:120])
    check(page.locator(".startrow button").first.inner_text().strip() == "开始",
          "one button, not two pairs of them")

    # The user's own case: one channel out of eight, at half duty.
    boxes = card.locator('.chvals input[type=checkbox]')
    for i in range(boxes.count()):
        if i != 2 and boxes.nth(i).is_checked():
            boxes.nth(i).uncheck()
            page.wait_for_timeout(120)
    # By channel, not by position: unticking a channel rebuilds the grid, so
    # ".first" can land on the box that was there a render ago - and the 50
    # then goes to DO1 while DO3 keeps the plan's 100. That is a test bug, but
    # it is the kind that reads as a product bug.
    sel = ('.card[data-port="dout"] .chvals[data-param="duty"] '
           'input[type=number][data-ch="3"]')
    for _ in range(50):
        if page.locator(sel).count() == 1:
            break
        page.wait_for_timeout(100)
    page.locator(sel).fill("50")
    page.wait_for_timeout(500)
    check(page.locator(sel).input_value() == "50",
          "the duty box for DO3 holds what was typed",
          page.locator(sel).input_value())

    row = page.locator(".startrow").first.inner_text()
    check("不给结论" in row, "the card says up front that it will not judge",
          " ".join(row.split())[:140])
    check("恢复方案参数" in row, "and offers to put the plan's parameters back", row)
    # 细节写在卡片顶上的说明块里（按钮旁边只留一句短的，免得同一段话印两遍）
    note = page.locator(".startrow .note").first.inner_text()
    check("DO3" in note and "50" in note, "and names what was changed",
          " ".join(note.split())[-200:] +
          "  |  edited=" + str(page.evaluate("JSON.stringify(edited)")))

    # Press it. The board is fine, so the one thing that must not happen is 失败.
    page.locator(".startrow button.primary").first.click()
    for _ in range(400):
        if "跑着" not in page.locator(".startrow").first.inner_text():
            break
        page.wait_for_timeout(100)
    page.wait_for_timeout(600)
    stat = page.locator('.prow[data-port="dout"] .stat').inner_text().strip()
    check(stat != "失败", "a healthy board is not reported as failed", stat)
    check("不给结论" in page.locator(".startrow").first.inner_text(),
          "and the card says why there is no verdict",
          page.locator(".startrow").first.inner_text())

    # And it has to be one click back to a state that does judge.
    for b in range(page.locator(".startrow button").count()):
        if "恢复" in page.locator(".startrow button").nth(b).inner_text():
            page.locator(".startrow button").nth(b).click()
            break
    page.wait_for_timeout(600)
    check("不给结论" not in page.locator(".startrow").first.inner_text(),
          "restoring the plan's parameters makes it judge again",
          " ".join(page.locator(".startrow").first.inner_text().split())[:120])


# What must never be printed on a card again. Every one of these was on screen
# on 2026-09-11, when the user said: 页面上英文的地方没有翻译成中文.
#
# *** Not a general "no ASCII" rule on purpose. Terminal numbers (A03-A10),
# *** part numbers (LAN8742A, ISO1044, LM50) and units (Hz, mV) are what the
# *** engineer reads off the board and the schematic - translating those would
# *** make the panel harder to use, not easier.
BANNED_ON_CARDS = [
    "Klemmblock", "duty", "freq", "period", "miss", "seq=", "rxlines",
    "hold", "blink", "extloop", "loopback", "autoneg", "mismatches",
    "observed", "detected", "Digital Out", "AOUT", "SD Karte", "SDram",
    "temp1", "temperature",
]


def check_nothing_in_english(page):
    """Every port card, swept for the protocol words a person should not meet.

    The panel is read in Chinese (user 2026-09-11). Nothing enforced that, so
    a field name added to the firmware arrived on screen in English and stayed
    there until somebody complained.
    """
    Section("no protocol words left on the cards")

    page.locator('.tab[data-tab="manual"]').click()
    page.wait_for_selector(".prow")
    rows = page.locator(".prow")
    ports = [rows.nth(i).get_attribute("data-port") for i in range(rows.count())]
    bad = {}
    for name in ports:
        if not name or not pick_port(page, name):
            continue
        body = page.locator("#panelbody").inner_text()
        # The card prints the protocol name once, in a row labelled 协议名.
        body = "\n".join(l for l in body.splitlines() if l.strip() != name)
        for w in BANNED_ON_CARDS:
            if w in body:
                bad.setdefault(w, []).append(name)
    check(not bad, "no card prints a protocol word at a person",
          "; ".join("%s on %s" % (w, ",".join(ps)) for w, ps in sorted(bad.items())))

    # The plan tab prints the keys of a JSON file, so it is where English
    # survives longest - the port cards at least pass through PARAM_CN first.
    # Labels are translated and the file's own spelling moved to the tooltip;
    # the VALUES stay as the file has them, because these boxes edit that file.
    page.locator('.tab[data-tab="plan"]').click()
    page.wait_for_timeout(500)
    rows = page.locator("#planlist .pf, #planlist .prow, #planlist div")
    if rows.count():
        rows.first.click()
        page.wait_for_timeout(800)
    steps = page.locator(".plansteps .st")
    if check(steps.count() > 0, "the plan tab lists the steps",
             "%d steps" % steps.count()):
        steps.first.click()
        page.wait_for_timeout(400)
        body = page.locator("#planbody").inner_text()
        leftover = [w for w in ("PtSession", "PtRun", "PtRaw", "UserConfirm",
                                "execute_condition", "timeout_ms", "retry_count",
                                "sleep_before_ms", "frames")
                    if w in body]
        check(not leftover, "the plan page prints no raw file key at a person",
              ", ".join(leftover))
        # The tooltip is the other half: hiding the key entirely would leave
        # nobody able to work out which line of the JSON a box edits.
        tip = page.locator("#planbody .k[title]").first
        check(tip.count() and "文件里的写法" in (tip.get_attribute("title") or ""),
              "and still says, on hover, what the file calls it",
              tip.get_attribute("title") or "")
    page.locator('.tab[data-tab="manual"]').click()
    page.wait_for_timeout(300)


def check_run_one_target(page):
    """Running one pt.run target without driving the rest of the port.

    On eth this is its own case now: the card is built per test case rather
    than per port (2026-09-14), and reading the PHY is case ① - it needs no
    network, no peer and no IP, which is exactly why somebody reaches for it
    on its own. Each case owns its button and its result, so this also checks
    that running one does not clear the other's.
    """
    Section("running one case on its own")

    if not check(pick_port(page, "eth"), "eth is in the port list"):
        return
    cases = page.locator("#panelbody .case")
    if not check(cases.count() == 2, "eth is split into its two cases",
                 "%d cases" % cases.count()):
        return
    first = cases.nth(0)
    check("读网口芯片" in first.locator(".casehd").inner_text(),
          "case ① is the one that needs no network",
          first.locator(".casehd").inner_text())
    # One button per case: the whole point of the rewrite is that "what can I
    # press" is answerable without reading the card twice.
    for i, want in ((0, 1), (1, 1)):
        n = cases.nth(i).locator(".caseact button").count()
        check(n == want, "case %d offers exactly one action" % (i + 1), "%d" % n)

    page.fill("#raw", "")           # so the log assertion below cannot match an echo
    first.locator(".caseact button").first.click()
    page.wait_for_timeout(3000)

    stat = page.locator('.prow[data-port="eth"] .stat').inner_text().strip()
    check(stat != "未测", "pressing it alone reaches a verdict", stat)
    log = page.locator("#log").inner_text()
    check("pt.run eth.link" in log,
          "and the command that went out is the one the case names",
          " ".join(log.split())[-160:])
    # The result belongs to case ① and stays there. One slot per port would
    # have the session overwrite it the moment anything else ran.
    check("过" in first.locator(".caseres").inner_text(),
          "the verdict lands in that case's own result row",
          first.locator(".caseres").inner_text())


def check_continuous_run(page):
    """持续测试：选时长、勾多个端口一起跑、看门狗续期。

    The board no longer times anything - the PC does, and it keeps the board's
    deadman renewed while it runs (DECISIONS.md 37). None of that is visible in
    a single command, so it is exercised here: the duration picker only appears
    under 持续, several ports start from one press, and pt.hold really goes out.
    """
    Section("continuous runs: duration, multi-select, the deadman")

    if not check(pick_port(page, "din"), "din is in the port list"):
        return

    # 单次 is the default, and the duration picker has no business there: a run
    # that lasts seconds does not need to be asked how many hours it should last.
    check(page.locator('.startrow input[data-mode="once"]').is_checked(),
          "单次 is the default mode")
    check(page.locator(".startrow .hours").count() == 0,
          "no duration picker under 单次")

    page.locator('.startrow input[data-mode="loop"]').check()
    page.wait_for_timeout(400)
    hours = page.locator(".startrow .hours input[data-hours]")
    check(hours.count() == 5,
          "持续 1/2/3/4 小时 and 一直跑 are the five choices",
          "%d choices" % hours.count())
    check(page.locator('.startrow input[data-hours="0"]').count() == 1,
          "一直跑 is one of them")

    # The multi-select bar. Two ports, one press.
    page.locator('.prow[data-port="din"] input[data-pick]').check()
    page.locator('.prow[data-port="temp"] input[data-pick]').check()
    page.wait_for_timeout(400)
    bar = page.locator("#batchbar")
    check(bar.is_visible(), "the batch bar shows once something is ticked")
    txt = bar.inner_text()
    check("数字量输入" in txt or "din" in txt or "一起跑" in txt,
          "and it names what was ticked", " ".join(txt.split())[:120])

    # 一直跑 so the run does not end while the assertions below are still going.
    bar.locator('input[data-hours="0"]').check()
    page.wait_for_timeout(300)
    page.fill("#raw", "")
    bar.locator('button[data-batch="start"]').click()

    page.wait_for_function(
        "() => { const b = document.querySelector('#batchbar button[data-batch=\\'stop\\']');"
        " return !!b; }", timeout=15000)
    Ok("PASS  one press started them and the bar turned into a stop button")

    log = page.locator("#log").inner_text()
    check("pt.start din" in log and "pt.start temp" in log,
          "both ports were actually started", " ".join(log.split())[-200:])
    # The deadman is the whole reason the board survives the PC dying.
    # ⚠️ Asserted on the board's REPLY: the server sends pt.hold on the serial
    # port itself, not through /api/command, so the outgoing line never reaches
    # the page's log - only what came back does.
    check("OK hold=" in log, "the deadman was armed",
          " ".join(log.split())[-200:])

    # The page's own buffer holds about four and a half minutes of a five-port
    # run. A burn-in has to leave something behind that outlives it, so the
    # server writes the run to a file as it goes - and the bar has to say where,
    # or nobody looks for it until after the four hours are gone.
    bartxt = page.locator("#batchbar").inner_text()
    check("run-" in bartxt and ".log" in bartxt,
          "the bar names the file the run is being written to",
          " ".join(bartxt.split())[:160])

    # Renewal: the span is 6 s and the server renews every 2 s, so a second
    # acknowledgement has to appear inside this wait. A run that armed it once and
    # then forgot would drop the outputs mid-test on a real bench.
    before = page.locator("#log").inner_text().count("OK hold=")
    page.wait_for_timeout(5000)
    after = page.locator("#log").inner_text().count("OK hold=")
    check(after > before, "and it is being renewed, not armed once",
          "%d -> %d" % (before, after))

    page.locator('#batchbar button[data-batch="stop"]').click()
    page.wait_for_timeout(1500)
    log = page.locator("#log").inner_text()
    check("OK stopped all" in log, "stopping the run stopped every port",
          " ".join(log.split())[-200:])
    check("OK hold=off" in log, "and disarmed the deadman",
          " ".join(log.split())[-200:])

    for name in ("din", "temp"):
        box = page.locator('.prow[data-port="%s"] input[data-pick]' % name)
        if box.count():
            box.uncheck()
    page.wait_for_timeout(300)


def check_autoecho_toggle(page):
    """The 自动回环应答 box in the header.

    Only the API had ever been exercised. The box is what a bench turns off
    when a second tool wants the control port, and a box that silently does
    nothing would look exactly like a board that stopped answering.
    """
    Section("the auto-echo box")

    box = page.locator("#autoecho")
    if not check(box.count() == 1, "the box is there once connected"):
        return
    was = box.is_checked()
    box.set_checked(not was)
    page.wait_for_timeout(600)
    check(box.is_checked() != was, "it toggles")
    stat = page.locator("#echostat").inner_text().strip()
    check(stat != "", "and says what state it is in now", stat)
    box.set_checked(was)
    page.wait_for_timeout(600)
    check(box.is_checked() == was, "and toggles back")


def check_peer_binding(page):
    """The 绑上 / 解开 pair on a loop=link port.

    ⚠️ What this can and cannot prove on the simulated board:
      - CAN prove: the control is there, it lists this machine's serial ports,
        binding reaches the server and comes back either bound or with a
        readable reason, and 解开 undoes it.
      - CANNOT prove: that binding the right adapter is what closes the loop.
        The simulated board answers its own link ports (its stimulate() plays
        every peer), so a bench cable is the only thing that can show that.
        Left to a real board on purpose rather than faked here.
    """
    Section("binding a peer serial port")

    if not check(pick_port(page, "rs485"), "rs485 is in the port list"):
        return
    sel = page.locator("#panelbody .chvals select")
    if not check(sel.count() >= 1, "the card offers a peer port to bind"):
        return
    options = sel.first.locator("option")
    n = options.count()
    check(n >= 1, "the list is filled from this machine's serial ports", "%d" % n)
    if n == 0:
        return

    com = options.first.get_attribute("value")
    sel.first.select_option(com)
    page.locator("#panelbody .chvals button", has_text="绑上").first.click()
    page.wait_for_timeout(1500)

    body = page.locator("#panelbody").inner_text()
    msg = page.locator("#panelbody .msg")
    if msg.count() and msg.first.inner_text().strip():
        # A refusal is a legitimate outcome here - the port may be in use by
        # something else on this machine. What matters is that it says so in
        # words rather than leaving the card looking bound.
        said = msg.first.inner_text().strip()
        check("绑上" not in body or "解开" not in body,
              "a refused binding does not leave the card looking bound", said)
        check(said != "", "and the refusal is stated in words", said)
        return

    check("解开" in body, "a bound peer offers to be unbound",
          " ".join(body.split())[:160])
    page.locator("#panelbody .chvals button", has_text="解开").first.click()
    page.wait_for_timeout(1000)
    body = page.locator("#panelbody").inner_text()
    check("绑上" in body, "and unbinding puts the chooser back")


def check_plan_runs_from_the_page(page):
    """Pressing 运行 on the plan tab.

    The plan tab is the production path - one press, every step, one report -
    and nothing in a browser had ever pressed it. Only the HTTP API was
    covered, which cannot see that the button is wired to it or that the
    per-step verdicts land back on the steps.

    bench-smoke is used because it needs nothing but the control cable and has
    no UserConfirm step to stop on.
    """
    Section("running a plan from the plan tab")

    page.locator('.tab[data-tab="plan"]').click()
    page.wait_for_timeout(600)
    buttons = page.locator("#planlist button")
    picked = False
    for i in range(buttons.count()):
        if "bench-smoke" in buttons.nth(i).inner_text():
            buttons.nth(i).click()
            picked = True
            break
    if not check(picked, "bench-smoke.json is offered on the plan tab"):
        return
    page.wait_for_selector("#planbody .plansteps .st")
    steps = page.locator("#planbody .plansteps .st")
    want = steps.count()
    check(want >= 5, "its steps are drawn", "%d steps" % want)

    run = page.locator("#planbody .card .hd button").first
    check(run.inner_text().strip() == "运行", "the run button is the first one",
          run.inner_text())
    run.click()

    # Every step judged, or the run is not finished. bench-smoke on the
    # simulated board is about half a minute; the wait is generous on purpose
    # because a slow run and a broken button look identical until it lands.
    got = 0
    for _ in range(180):
        got = page.locator("#planbody .plansteps .vd").count()
        if got >= want:
            break
        page.wait_for_timeout(1000)
    if not check(got >= want, "pressing 运行 runs every step",
                 "%d of %d steps came back" % (got, want)):
        return

    marks = page.locator("#planbody .plansteps .vd")
    outcomes = [marks.nth(i).inner_text().strip() for i in range(marks.count())]
    check(all(o in ("PASS", "SKIPPED") for o in outcomes),
          "and every step of the smoke plan passes on the simulated board",
          " ".join(outcomes))

    page.locator('.tab[data-tab="manual"]').click()
    page.wait_for_timeout(400)


def check_aout_walk(page):
    """The multi-point analog-output walk, with the meter reading typed in.

    Two things are only decidable in a browser. One is that the prompt asking
    for the meter reading SURVIVES - the panel rebuilds itself on every frame,
    and an input inside that region is destroyed under the person's hands while
    they are still typing. The other is that the verdict uses percent of full
    scale: at the bottom of the range one DAC step is already a large fraction
    of the reading, so a percent-of-reading limit is one this hardware cannot
    meet there.
    """
    Section("AOUT multi-point walk")

    page.locator('.tab[data-tab="manual"]').click()
    page.wait_for_selector(".prow")
    rows = page.locator(".prow")
    picked = False
    for i in range(rows.count()):
        if rows.nth(i).get_attribute("data-port") == "aout":
            rows.nth(i).click()
            page.wait_for_timeout(300)
            picked = True
            break
    if not check(picked, "aout is in the port list"):
        return

    # One card, two uses, told apart by 读数来自 (DECISIONS 47). The walk only
    # exists once that is set to 人工读表 - before then this is the echo-only
    # card a production plan runs unattended.
    card = page.locator('.card[data-port="aout"]')
    if not check(card.count() > 0, "the aout card is there"):
        return
    pick = card.locator('input[type=radio][data-meter="manual"]')
    if not check(pick.count() > 0, "the card offers 读数来自 as a choice"):
        return
    check(card.locator('input[type=radio][data-meter="none"]').count() > 0,
          "and 不读表 is the other one - the档 a plan runs")
    pick.first.check()
    page.wait_for_timeout(400)

    card = page.locator('.card[data-port="aout"]')
    check(card.locator('button.aocalgo').count() > 0,
          "picking 人工读表 turns the button into the walk")

    # Two points, so the walk is short but still more than one - one point
    # cannot separate an offset from a gain error, which is why it is a walk.
    card.locator('input[data-aocal="points"]').fill("1, 20")
    card.locator('input[data-aocal="tol"]').fill("2")
    page.wait_for_timeout(150)

    card.locator("button.aocalgo").click()

    # The prompt has to come up, and it has to still be there after frames have
    # rebuilt the panel underneath it.
    ask = page.locator("#meterask")
    try:
        ask.wait_for(state="visible", timeout=15000)
    except Exception:
        check(False, "the walk asks for a meter reading", "#meterask never appeared")
        return
    check(True, "the walk stops and asks for a meter reading")

    first_text = page.locator("#meterwhat").inner_text()
    check("mA" in first_text, "the prompt says what the board is putting out", first_text)

    # Sit through several frames. This is the assertion that matters: a prompt
    # built inside #panelbody would have been rebuilt away by now.
    page.wait_for_timeout(2500)
    check(ask.is_visible(), "and it is still there after the panel has redrawn")
    typed = page.locator("#meterval")
    typed.fill("0.900")
    check(typed.input_value() == "0.900",
          "what was typed survives the redraws too", typed.input_value())

    # 0.900 against a 1 mA target is -0.1 mA, which is -0.5% of a 20 mA full
    # scale - outside the 2% asked for? No: it is inside. Deliberately, so the
    # first point passes and the second is the one that fails.
    page.locator("#meterok").click()
    try:
        ask.wait_for(state="visible", timeout=15000)
    except Exception:
        check(False, "it moves on to the second point")
        return
    check(True, "it moves on to the second point")
    page.locator("#meterval").fill("18.0")     # 2 mA low on 20 = -10% FS
    page.locator("#meterok").click()
    page.wait_for_timeout(1500)

    card = page.locator('.card[data-port="aout"]')
    body = card.inner_text()
    check("过" in body and "不过" in body,
          "the table shows one point passing and one failing", body[-300:])
    check("% FS" in body,
          "and the verdict is stated against full scale, not against the reading",
          body[-300:])
    # The walk now fits a line through the points it collected (internal/ptcal,
    # 2026-09-13). Two points is the case the fit itself flags as Exact: they
    # define a line, so a zero residual there is arithmetic and not a
    # measurement that agreed - and the card has to say so, or somebody reads a
    # perfect calibration off two readings.
    check("增益" in body and "零点偏差" in body,
          "the card reports the gain and the offset it fitted", body[-400:])
    check("最大残差" in body,
          "and the residual, which is what says whether a line was the right shape",
          body[-400:])
    check("两个点" in body,
          "two points are called out as defining their own line", body[-400:])
    # The tooling firmware never writes flash (CAL-04): the card has to say the
    # coefficients go to a file for station 10, and a simulated board's fit is
    # not archived at all - its readings are invented.
    check("不写进板子" in body,
          "the card says the coefficients are not written to the board",
          body[-400:])
    check("模拟板" in body and "不存档" in body,
          "and a simulated board's fit is not archived", body[-400:])


def check_ain_walk(page):
    """The multi-point analog-input walk (AI1 in mV, AI2 in mA).

    The AO walk's twin, the other way round: a person sets a signal source and
    types what it gives, the board's reading is the nominal. What only a
    browser shows is that the card exists, asks per point, fits, and says a
    simulated board's fit is not archived.
    """
    Section("AIN multi-point walk")
    page.locator('.tab[data-tab="manual"]').click()
    page.wait_for_selector(".prow")
    rows = page.locator(".prow")
    picked = False
    for i in range(rows.count()):
        if rows.nth(i).get_attribute("data-port") == "ain":
            rows.nth(i).click()
            page.wait_for_timeout(300)
            picked = True
            break
    if not check(picked, "ain is in the port list"):
        return
    card = page.locator('.card[data-port="ain"]')
    pick = card.locator('input[type=radio][data-aimeter="manual"]')
    if not check(pick.count() > 0, "the ain card offers 人工给信号 as a choice"):
        return
    pick.first.check()
    page.wait_for_timeout(400)
    card = page.locator('.card[data-port="ain"]')
    if not check(card.locator("button.aicalgo").count() > 0,
                 "picking 人工给信号 turns the button into the walk"):
        return
    card.locator('input[data-aical="points"]').fill("0, 10000")
    page.wait_for_timeout(150)
    card.locator("button.aicalgo").click()

    # The simulated board cannot follow a signal source, so each point is set on
    # it first (sim.ain takes the pin millivolts; AI1's divider is 90.6/22.6).
    base = page.url.split("#")[0].rstrip("/")
    ask = page.locator("#meterask")
    for n, (value, pin_mv) in ((1, ("1", 0)), (2, ("9990", 2494))):
        try:
            ask.wait_for(state="visible", timeout=15000)
        except Exception:
            check(False, "the walk asks for point %d" % n, "#meterask never appeared")
            return
        # Set only once the prompt is up: the page samples the previous point
        # after OK, and the next prompt appears when that sampling is done.
        page.request.post(base + "/api/command", data={"cmd": "sim.ain 1 %d" % pin_mv})
        page.wait_for_timeout(600)
        text = page.locator("#meterwhat").inner_text()
        check("AI1" in text and "mV" in text, "point %d names the channel and its unit" % n, text)
        page.locator("#meterval").fill(value)
        page.locator("#meterok").click()
    page.wait_for_timeout(4000)

    card = page.locator('.card[data-port="ain"]')
    body = card.inner_text()
    check("没读到" not in body, "every point got a board reading", body[-300:])
    check("增益" in body and "最大残差" in body,
          "the card reports the fit, residual first", body[-400:])
    check("两个点" in body, "two points are called out as defining their own line", body[-400:])
    check("不写进板子" in body, "the card says the coefficients are not written to the board", body[-400:])
    check("模拟板" in body and "不存档" in body,
          "and a simulated board's fit is not archived", body[-400:])


def check_limits_are_readonly(page):
    """The plan tab's limits, and the save that could put one back.

    This is where the limits actually live, and until 2026-09-10 nothing looked
    at it: the only assertion was on the port tab, for an attribute nothing ever
    set, so it held whatever the plan tab did. The plan tab was in fact drawing
    every op/min/max/value/unit as a text box, and saving wrote the lot back -
    the opposite of DECISIONS.md 30 and 34, under a green light.

    Two halves, because a page is only half the protection. What is on screen
    is the operator's side; what the server accepts is everybody else's, and a
    request does not have to come from this page.
    """
    import json

    Section("limits are read-only, and unsavable")

    page.locator('.tab[data-tab="plan"]').click()
    page.wait_for_selector("#planlist button")
    plans = page.locator("#planlist button")
    name = plans.nth(0).inner_text().strip()
    plans.nth(0).click()
    page.wait_for_selector("#planbody .plansteps .st")

    # The first step that has limits at all. A step without them would make
    # every assertion below vacuously true.
    steps = page.locator("#planbody .plansteps .st")
    shown = False
    for i in range(steps.count()):
        steps.nth(i).click()
        page.wait_for_timeout(120)
        if page.locator("#planbody .pgrid .ro").count() > 0:
            shown = True
            break
    if not check(shown, "a step's limits are drawn on the plan tab"):
        return

    # Everything between the 判据 heading and the next one belongs to the
    # limits. Counted in the page rather than by selector because the grid is
    # flat - the heading is the only boundary there is.
    editable = page.evaluate(
        """() => {
            const g = document.querySelector('#planbody .pgrid');
            let inside = false, n = 0;
            for (const el of g.children) {
              if (el.classList.contains('pgroup')) { inside = el.textContent.includes('判据'); continue; }
              if (inside) n += el.querySelectorAll('input,select,textarea').length;
            }
            return n;
        }"""
    )
    check(editable == 0,
          "not one limit on the plan tab is typeable", "%d editable" % editable)

    # And the server refuses one anyway. Sent the way anything else would send
    # it, not through the page: the page is what is being taken out of the
    # trust chain here.
    # Absolute: the request context has no page to resolve a relative URL
    # against, whatever the page happens to be showing.
    base = page.url.split("#")[0].rstrip("/")
    got = page.request.get(base + "/api/plan?name=" + name).json()
    if not check("plan" in got, "the plan loads over the API", json.dumps(got)[:200]):
        return

    # The bytes, kept so this run leaves the shipped plan exactly as it found
    # it. Saving through the panel reflows the file and quotes its numbers -
    # harmless to the loader, and still not something a test should leave in a
    # file somebody else's run will diff.
    on_disk = panel_exe().parent / "plans" / name
    original = on_disk.read_bytes() if on_disk.exists() else None
    plan = got["plan"]
    idx = next((i for i, s in enumerate(plan["steps"]) if s.get("checks")), None)
    if not check(idx is not None, "the plan has a step with limits"):
        return

    was_checks = json.loads(json.dumps(plan["steps"][idx]["checks"]))
    was_version = plan["limit_version"]
    was_timeout = plan["steps"][idx].get("timeout_ms")

    # A parameter to move as well, on whichever step has one. Without a
    # legitimate edit in the same request, a save that refused everything
    # outright would pass every assertion below.
    pidx = next((i for i, s in enumerate(plan["steps"]) if s.get("params")), None)
    pkey = pval = None
    if pidx is not None:
        pkey = sorted(plan["steps"][pidx]["params"])[0]
        pval = str(plan["steps"][pidx]["params"][pkey])
        plan["steps"][pidx]["params"][pkey] = pval + "0"

    plan["steps"][idx]["checks"][0]["value"] = "ANYTHING"
    plan["steps"][idx]["checks"][0]["op"] = "contains"
    plan["limit_version"] = "forged"
    plan["steps"][idx]["timeout_ms"] = (was_timeout or 20000) + 1234

    saved = page.request.post(base + "/api/plan", data={"name": name, "plan": plan}).json()
    check("error" not in saved or not saved["error"],
          "the save itself is accepted", json.dumps(saved)[:200])

    back = page.request.get(base + "/api/plan?name=" + name).json()["plan"]
    now = back["steps"][idx]
    check(now["checks"] == was_checks,
          "a widened limit did not survive the save - the file's own limits won",
          json.dumps(now["checks"])[:200])
    check(back["limit_version"] == was_version,
          "and limit_version is still the one the file had",
          "%s -> %s" % (was_version, back["limit_version"]))
    check(now.get("timeout_ms") == (was_timeout or 20000) + 1234,
          "while the parameter edit in the same save did go through",
          str(now.get("timeout_ms")))

    # And the log says which parameter moved and what it moved from. The file
    # only shows where it ended up; which readings were taken before the change
    # is what the log answers and the file cannot.
    if pidx is not None:
        want = "%s %s %s -> %s0" % (plan["steps"][pidx]["id"], pkey, pval, pval)
        page.wait_for_timeout(400)
        check(want in page.locator("#log").inner_text(),
              "and the log names the parameter that moved, and what it moved from",
              want)

    # Put the file back byte for byte, not by saving it again: a second save
    # would leave the reflow behind, which is the thing being avoided.
    if original is not None:
        on_disk.write_bytes(original)
    check(original is None or on_disk.read_bytes() == original,
          "and the plan file is left exactly as it was found")

    page.locator('.tab[data-tab="manual"]').click()


def check_remembered(saved, com):
    Section("what it remembered")
    import json

    if com.strip().lower() == "sim":
        # The opposite assertion, and the one that matters more.
        #
        # The simulated board must NOT be written down: it is not an adapter
        # anybody wired up, and recording it overwrites the answer to a
        # question only a person at the bench can give. That is not
        # hypothetical - a run of this test against sim on 2026-09-08 replaced
        # a bench's rs232/rs485/can mapping with {"control":"sim"}, and the
        # mapping had to be rebuilt by hand.
        if not saved.exists():
            check(True, "nothing was written down for the simulated board")
            return
        got = json.loads(saved.read_text(encoding="utf-8"))
        check(got.get("control", "").lower() != "sim",
              "the simulated board is not written down as a control port",
              json.dumps(got, ensure_ascii=False))
        check(got.get("peers") is not None,
              "and whatever a bench had bound is left alone",
              json.dumps(got, ensure_ascii=False))
        return

    # Asked for on 2026-09-08: work out which adapter is which once, not every
    # time the panel is opened.
    if check(saved.exists(), "the control port is written down after connecting",
             str(saved)):
        got = json.loads(saved.read_text(encoding="utf-8"))
        check(got.get("control") == com,
              "the port written down is the one that was used",
              json.dumps(got, ensure_ascii=False))


def run_checks(page, com):
    # ---------------------------------------------------- before connecting
    Section("before a control port is chosen")
    page.wait_for_selector("#portlist .p")
    rows = page.locator("#portlist .p")
    check(rows.count() > 0, "the serial port list is shown, not hidden in a dropdown",
          "%d rows" % rows.count())
    # R4-03: a port name alone does not say which adapter it is - on Linux a
    # bare ttyUSB0 says nothing at all. A row with USB ids must also say what
    # the device calls itself or which driver took it.
    unnamed = [t for t in (rows.nth(i).inner_text().strip() for i in range(rows.count()))
               if "[" in t and " - " not in t and " (" not in t]
    check(not unnamed, "every USB port row says what the adapter is", "; ".join(unnamed))
    check(page.locator("#connect").is_disabled(),
          "connect is refused until a port is picked")
    check(page.locator("#portlist .p.on").count() == 0,
          "nothing is preselected on a first run")

    # The simulated board has to be offered in the list, and has to be reachable
    # without scrolling. It is appended after every real port, so on a bench
    # with a handful of USB adapters it is the entry most easily buried - and it
    # is the one entry that lets somebody look at the panel with no hardware at
    # all. It also has to be visibly not a COM port: everything it reports is
    # invented, and a row that reads like the ones above it invites a simulated
    # run being filed as a bench result.
    sim = page.locator('#portlist .p[data-port="sim"]')
    if sim.count():
        check("sim" in (sim.first.get_attribute("class") or ""),
              "the simulated board is marked apart from the real ports",
              sim.first.get_attribute("class"))
        box, list_box = sim.first.bounding_box(), page.locator("#portlist").bounding_box()
        check(box and list_box and box["y"] + box["height"] <= list_box["y"] + list_box["height"] + 1,
              "and is visible without scrolling the list",
              "row bottom %.0f vs list bottom %.0f" % (
                  (box or {}).get("y", -1) + (box or {}).get("height", 0),
                  (list_box or {}).get("y", -1) + (list_box or {}).get("height", 0)))
    else:
        # Not a failure: a machine with no simulator built has nothing to show.
        # Said out loud so a silent absence is never mistaken for a pass.
        check(True, "no simulated board built on this machine - nothing to check")

    # The whole rest of the page is inert: everything on it is a reading that
    # arrives over the control port, so before that port is open there is
    # nothing on it that could mean anything.
    gate = page.locator("#afterconn")
    check("live" not in (gate.get_attribute("class") or ""),
          "the rest of the page is gated off before connecting")

    # ---------------------------------------------------- pick and connect
    Section("choosing the control port")
    picked = False
    for i in range(rows.count()):
        if com in rows.nth(i).inner_text():
            rows.nth(i).click()
            picked = True
            break
    if not check(picked, "the control port %s is in the list" % com):
        return

    check(not page.locator("#connect").is_disabled(),
          "connect becomes available once a port is picked")
    check(com in page.locator("#connhint").inner_text(),
          "the hint names the port that was picked")

    page.locator("#connect").click()
    page.wait_for_selector("#afterconn.live")
    check(True, "connecting opens the rest of the page")
    status = page.locator("#status").inner_text()
    check("porttool" in status.lower() or re.search(r"\d+\.\d+\.\d+", status),
          "the header shows the firmware version the board reported", status)

    # ---------------------------------------------------- the port list
    Section("the port list the board reported")
    page.wait_for_selector(".prow")
    heads = page.locator(".boardhd")
    labels = [heads.nth(i).inner_text() for i in range(heads.count())]
    check(heads.count() >= 3, "ports are grouped by which board they are on",
          str(labels))
    for want in ("Bridge", "Upper Deck", "Lower Deck"):
        check(any(want in l for l in labels), "there is a %s group" % want, str(labels))

    prows = page.locator(".prow")
    n = prows.count()
    check(n >= 15, "every port the board reported has a row", "%d rows" % n)

    # Untested is the honest starting state, and it has to be visible: a port
    # that looks the same tested and untested is a port somebody will skip.
    first = prows.nth(0).inner_text()
    check("未测" in first, "a port starts as untested", first)

    # ---------------------------------------------------- clicking each port
    Section("clicking every port row")
    for i in range(n):
        name = prows.nth(i).locator(".nm").inner_text()
        prows.nth(i).click()
        cls = prows.nth(i).get_attribute("class") or ""
        if not check("on" in cls.split(), "%s selects when clicked" % name, cls):
            continue
        body = page.locator("#panelbody").inner_text()
        check(name in body,
              "%s selected shows that port in the middle column" % name,
              body[:80].replace("\n", " "))

    # -------------------------------------------- the one button that matters
    #
    # The whole page exists so somebody can pick a port, press one button and
    # read a verdict. This presses it on EVERY port and checks the verdict is
    # the right one - not merely that some verdict appeared.
    #
    # Asserting only "a verdict showed up" is what let a real bug through on
    # 2026-09-08: sdram reported failed because the host's 3 s command timeout
    # was shorter than the 6.5 s sweep, and a weaker check called that a pass.
    #
    # EXPECT says what this bench should produce. A port that needs wiring this
    # bench does not have is expected to FAIL, and the reason is checked too -
    # "it failed" is not good enough when the point is that it failed for the
    # right reason.
    Section("one port, one press - every port")

    EXPECT = {
        # Need nothing wired. These must pass, and a failure here is a real one.
        "sdram": ("pass", None),
        "temp":  ("pass", None),
        "rtc":   ("pass", None),
        "led":   ("pass", None),
        "can":   ("pass", None),      # mode=extloop needs no peer
        "dout":  ("pass", None),      # drives open terminals, judged on drive only
        "relay": ("pass", None),      # ditto, no contact read-back to judge
        # Need something this bench has not got. Expected to fail, for a
        # reason that names the missing thing.
        # detected=0 when the slot is empty, mounted=0 when the card is
        # there but exFAT. Either is a fail; the reason names which.
        "din":   ("fail", "位图"),      # nothing is driving the inputs
        # AI hardware is being reworked; not judged until it is back.
        # See $PROD/work/TODO.md.
        "ain":   ("either", None),
        # *** eth used to be expected to fail here. *** It needed a TCP peer and
        # nothing opened one, so conn stayed 0. The panel became that peer on
        # 2026-09-14 (autoPeer): it reads the address out of the board's own
        # frames and connects itself, which is what the command-line runner had
        # been doing all along. So it passes now, and a failure is a real one.
        "eth":   ("pass", None),
        # Both of these need something plugged in that this bench now has: a
        # card in the slot, and the CH340 on C10/C11 bound through the card's
        # own peer picker (which this sweep does, below). Expecting them to fail
        # was a statement about the bench, and the bench changed.
        "sd":    ("pass", None),
        "rs485": ("pass", None),
        "knx":   ("either", None),      # bus power is the operator's business
        # rs232 got criteria on 2026-09-08 after this sweep found it had none.
        "rs232": ("pass", None),
        "aout":  ("pass", None),        # judged on the DAC value, not current
        # *** pwm and bringup used to sit here as ("manual", None). *** They
        # left pt.caps on 2026-09-13, so the page has no row for them at all
        # (DECISIONS.md 40) and there is nothing left to expect. The "manual"
        # branch below stays: it is what any future row with no judgeable
        # reading has to look like, and it is cheaper to keep than to rebuild.
        #
        # sdram became a session the same day, and station 6 gained the step
        # that judges it (sdram-refresh) in the same change - without one the
        # panel reports the port unjudged forever, which is the hole eth and sd
        # both fell into.
        "sdram": ("pass", None),
    }

    if com.strip().lower() == "sim":
        # The simulated board is wired by construction: a fixture holds the
        # digital inputs high, the analog inputs sit inside their bands, and
        # its stimulate() plays every link port's peer. So the four ports that
        # fail for want of wiring on a bench have to pass here.
        #
        # This is not the assertion being relaxed - it is the same assertion
        # against a different bench. Left expecting failure, a simulated run
        # would be red whatever the software did, which is the same as not
        # running it at all.
        for k in ("din", "ain", "rs485", "sd"):
            EXPECT[k] = ("pass", None)
        # The simulated board answers its own CDC pipe. On a bench the peer is
        # the COM port the board enumerates as, bound like any other link port.
        EXPECT["usb"] = ("pass", None)
        # Its session half needs a peer holding an address, so station 6 ships
        # that step disabled - which leaves the session unjudged and the port
        # with it. The one-shot half still has to pass.
        EXPECT["eth"] = ("either", None)

    prows = page.locator(".prow")
    tested = []
    for i in range(prows.count()):
        key = prows.nth(i).get_attribute("data-port")
        want, want_why = EXPECT.get(key, ("either", None))

        if not page.locator("#afterconn.live").count():
            Fail("the panel lost the board - stopping instead of timing out "
                 "on every remaining port")
            failures.append("connection lost mid-sweep")
            return

        print("  ... %s" % key, flush=True)
        prows.nth(i).click()
        # 每个用例自己一个主按钮，都在 .caseact 里 —— 会话那一段的按钮外面还裹了
        # 一层 .startrow（单次/持续那一组要跟着它），所以两种都找。
        # *** 限定在这个端口自己那张卡里。*** #panelbody 里还挂着挂在端口下面、
        # 却不属于它任何一个用例的卡（今天只剩模拟输出那张多点测量），它的按钮
        # 也是 .caseact button.primary —— 不限定就会顺手点了「走一遍」，而那一轮
        # 会停下来等人读万用表，这一遍扫描就卡在那儿。
        card = '.card[data-port="%s"]' % key
        btn = page.locator(card + " .caseact button.primary")
        if btn.count() == 0:
            check(want == "manual",
                  "%s offers no start button - it can only be judged by eye" % key)
            check(page.locator(".prow.on .stat").inner_text().strip() == "人工判",
                  "%s says 人工判 rather than 未测" % key,
                  page.locator(".prow.on .stat").inner_text())
            continue

        # 对端要人插适配器的端口，面板现在不肯替他选 —— 它分不出哪个适配器接在
        # 哪个端子上，猜错了报出来的失败会像板子的毛病（用户 2026-09-14）。所以
        # 这里也照人做的来：先在卡片上把对端绑好，再点开始。
        # 真台子上要人先把适配器选好再开始（面板分不出哪个插在哪个端子上）。
        # 模拟板自带对端，绑一个真串口反而是错的 —— 那会去开工位上另一块板的线。
        if com.strip().lower() != "sim":
            bind = page.locator("#panelbody .peerbox button", has_text="绑上")
            if bind.count():
                bind.first.click()
                page.wait_for_timeout(1200)

        # 绑完对端这张卡重画过了，重新找一遍。
        btn = page.locator(card + " .caseact button.primary")
        # *** 每一段都要按。*** 一个口有五段用例时，只按第一段等于只测了五分之
        # 一，而端口状态会照样变绿 —— 那正是这一轮扫描要防的事。
        for b in range(btn.count()):
            one = page.locator(card + " .caseact button.primary").nth(b)
            if not one.count():
                break
            one.click()
            # 一次性的那几段不走 testing[]，按钮上不会写「跑着」，等待条件会立刻
            # 放行 —— 那就会在结果出来之前读状态。所以等这一段自己的结论变掉。
            try:
                page.wait_for_function(
                    "(a) => { const r = document.querySelectorAll("
                    "             '.card[data-port=\"' + a.port + '\"] [data-case]');"
                    "         return r[a.n] && r[a.n].textContent.indexOf('还没跑') < 0; }",
                    arg={"port": key, "n": b}, timeout=90000)
            except Exception:
                pass

        budget = 90000
        page.wait_for_function(
            # 单动作的口没有 .startrow —— 它的按钮在目标行里。写成「面板里没有
            # 任何按钮还写着『跑着』」就两种都盖到了。
            "() => ![...document.querySelectorAll('#panelbody button')]"
            "        .some(b => b.textContent.indexOf('跑着') >= 0)", timeout=budget)

        # 每一段自己的结论都挂着 data-case，把它们连起来就是这个口的全部说法。
        #
        # *** 不再读 .startrow 里的第一个 .sub。*** 那一行里「单次 / 持续」两个
        # 单选各自带一句说明，也是 .sub，而且排在状态前面 —— 读到的会是
        # 「跑一轮，跑完自动停下」，不是结论。
        notes = page.locator(card + " [data-case]")
        note = " | ".join(notes.nth(j).inner_text().strip()
                          for j in range(notes.count()))
        status = page.locator(".prow.on .stat").inner_text().strip()
        tested.append((key, status, note))

        if want == "pass":
            check(status == "通过",
                  "%s passes on this bench" % key, "%s / %s" % (status, note))
        elif want == "fail":
            ok = check(status == "失败",
                       "%s fails on this bench, as it should" % key,
                       "%s / %s" % (status, note))
            if ok and want_why:
                check(want_why in note,
                      "%s says which reading was wrong" % key, note)
        elif want == "manual":
            pass                        # settled above, before the button

        else:
            check(status in ("通过", "失败"),
                  "%s reaches a verdict either way" % key,
                  "%s / %s" % (status, note))

    Section("what the bench produced")
    for key, status, note in tested:
        print("  %-9s %-6s %s" % (key, status, note[:70]))

    # The one-button test has to start each port the way the plan starts it,
    # including the per-channel parameters. Checked against what actually went
    # down the wire, because getting this wrong does not look like a bug: the
    # board answers happily, having driven nothing, and a healthy board is
    # reported as failed. That is what happened on 2026-09-08 - the panel
    # prefilled the scalar parameters from the plan and left on=/mv=/duty= at
    # the board's defaults of zero.
    Section("the plan's parameters really got sent")
    sent = page.locator("#log").inner_text()
    for want, why in (
            ("on=1:1", "relay is told to close its contacts, not just to hold"),
            ("mv=1:1000", "the analog outputs are told which voltage to produce"),
            ("duty=1:100", "the high-side outputs are told to drive"),
            ("mode=extloop", "can is started in the mode its criteria assume")):
        check(want in sent, "%s (%s)" % (want, why))

    # ---------------------------------------------------- the tabs
    Section("the tabs")
    page.locator('.tab[data-tab="plan"]').click()
    # The list is filled by a fetch, so waiting for a row rather than asserting
    # straight after the click. Asserting immediately is what made this fail
    # the first time - a test bug, not a panel bug.
    page.wait_for_selector("#planlist button")
    check(page.locator("#planlist").is_visible(),
          "the plan tab shows the plan list")
    check(page.locator("#planwhat").is_visible(),
          "the plan tab says in one line what a plan is for")
    plans = page.locator("#planlist button")
    check(plans.count() >= 1, "the plans shipped beside the exe are listed",
          "%d plans" % plans.count())

    # Clicking a plan has to put it in the middle column, the same column a
    # port uses - one rule for that column, whatever is selected.
    plans.nth(0).click()
    page.wait_for_timeout(400)
    check(page.locator("#planbody").inner_text().strip() != "",
          "a plan selected shows its steps in the middle column")
    check(not page.locator("#treebody").is_visible(),
          "the port list is put away while the plan tab is open")

    page.locator('.tab[data-tab="manual"]').click()
    check(page.locator("#treebody").is_visible(), "the port tab comes back")

    # ---------------------------------------------------- the log controls
    Section("the log pane")
    page.locator("#pause").click()
    check(page.locator("#paused").is_visible(),
          "pausing the log says so - it pauses drawing, not reading")

    # *** Timed, because the way this broke was invisible. *** Resuming
    # rebuilds the whole pane, and the first version of that did it by calling
    # appendLine in a loop - which reads scrollHeight after every insert and so
    # forces a synchronous layout per line. Measured 21.7 s for 4000 lines on
    # 2026-09-13; the page is frozen for all of it, and what the browser test
    # reported was a click that timed out, which says nothing about why. The
    # bound is loose on purpose: this is here to catch a return to per-line
    # layout, not to police milliseconds.
    t0 = time.time()
    page.locator("#pause").click()
    check(not page.locator("#paused").is_visible(), "resuming clears that")
    resume_ms = (time.time() - t0) * 1000
    check(resume_ms < 3000,
          "resuming the log redraws in one go, not a layout per line",
          "took %.0f ms - see redrawLog()" % resume_ms)

    page.locator("#clear").click()
    check(page.locator("#log").inner_text().strip() == "" or True,
          "clearing the log does not throw")

    for box in page.locator(".filt").all():
        box.uncheck()
        box.check()
    check(True, "every log filter can be switched off and on")

    # Exporting the log. Asked for on 2026-09-08: it has to come out as .txt,
    # because that is what opens by double-clicking and what a mail client will
    # accept as an attachment.
    try:
        with page.expect_download(timeout=10000) as dl:
            page.click("#save")
        got = dl.value
        name = got.suggested_filename
        check(name.endswith(".txt"), "the log exports as .txt", name)
        saved = Path(tempfile.gettempdir()) / "porttool_export_check.txt"
        got.save_as(str(saved))
        body = saved.read_text(encoding="utf-8", errors="replace")
        check(body.startswith("PortTool log"),
              "the exported file says what it is on the first line",
              body.splitlines()[0] if body else "(empty)")
        # A log exported to send somebody has to carry which port and which
        # firmware produced it, or it cannot be matched to a board later.
        check("control port:" in body and "firmware:" in body,
              "the export records the port and the firmware it came from")
        check(len(body.splitlines()) > 4,
              "the export has the log lines under that header",
              "%d lines" % len(body.splitlines()))
    except Exception as exc:                        # noqa: BLE001
        Fail("FAIL  exporting the log threw: %s" % str(exc).splitlines()[0])
        failures.append("log export")

    # -------------------------------------------------- which limits judged
    #
    # A tick on this page means nothing unless it says which limit set produced
    # it: the same board passes under one and fails under another. And limits
    # are switched by choosing a whole named plan, never by editing a number on
    # screen - the production guide bans passing a board by widening a limit,
    # and an edited box leaves no trace, while a plan name and its
    # limit_version go into every report.
    Section("which limits the verdicts came from")

    crit = page.locator("#critbox")
    check(crit.count() > 0 and crit.is_visible(),
          "the page says which limits it is judging by, in the header")
    sel = page.locator("#critplan")
    strict = sel.input_value()
    check(strict.endswith(".json"), "a plan file supplies the limits", strict)
    ver_before = page.locator("#critver").inner_text()
    check("限值" in ver_before,
          "and shows that plan's limit_version, not just its name", ver_before)

    options = sel.locator("option").all_text_contents()
    relaxed = next((o for o in options if "relaxed" in o), None)
    if check(relaxed is not None,
             "a relaxed limit set is offered next to the strict one",
             ", ".join(options)):
        # Give the page a verdict to lose, so "switching clears them" is
        # actually observable rather than vacuously true.
        page.fill("#raw", "pt.run rtc.read")
        page.click("#send")
        page.wait_for_timeout(600)

        sel.select_option(relaxed)
        page.wait_for_timeout(800)
        check(sel.input_value() == relaxed,
              "choosing the relaxed set switches to it", sel.input_value())
        ver_after = page.locator("#critver").inner_text()
        check(ver_after != ver_before,
              "the limit_version on screen changes with it",
              "%s -> %s" % (ver_before, ver_after))
        check(page.locator(".prow .stat", has_text="通过").count() == 0,
              "the verdicts from the old limits are cleared, not left looking current")
        log = page.locator("#log").inner_text()
        check("判据换成" in log,
              "and the log records the change, so a report can be traced to it")

        sel.select_option(strict)
        page.wait_for_timeout(600)
        check(sel.input_value() == strict, "and it switches back")

    # No editable limit anywhere: that is the property being protected.
    check(page.locator("#panelbody input[data-limit]").count() == 0,
          "no limit is editable on the port tab")

    check_limits_are_readonly(page)
    check_warns_before_you_press(page)
    check_channel_labels(page)
    check_off_plan_params(page)
    check_nothing_in_english(page)
    check_run_one_target(page)
    check_continuous_run(page)
    check_autoecho_toggle(page)
    check_peer_binding(page)
    check_plan_runs_from_the_page(page)
    check_aout_walk(page)
    check_ain_walk(page)

    # ---------------------------------------------------- KNX frame mode
    #
    # The point of mode=frames is that a person can put a real KNX frame on the
    # bus from this page and read back what the bus said - and that the page
    # answers "which reading of the octets is the real frame" with the check
    # octet rather than leaving it to the eye.
    #
    # ⚠️ This transmits on the installation, to group address 31/7/255. That
    # address is the default because it is the corner of the address space a
    # real project is least likely to have assigned; nothing subscribes to it.
    Section("KNX frame mode")

    def pick_port(name):
        rows = page.locator(".prow")
        for i in range(rows.count()):
            if rows.nth(i).get_attribute("data-port") == name:
                rows.nth(i).click()
                page.wait_for_timeout(200)
                return True
        return False

    if not check(pick_port("knx"), "knx is in the port list"):
        pass
    else:
        # The address controls exist because the board advertised them as
        # parameters - nothing about knx is written into the page.
        # A parameter is either a typed field (in .row) or a set of choices
        # (its own block). Both carry data-param, so this does not care which.
        holders = page.locator("#panelbody [data-param]")
        names = [holders.nth(i).get_attribute("data-param")
                 for i in range(holders.count())]
        for want in ("ga", "src", "val"):
            check(want in names,
                  "the card offers %s, straight from what the board accepts" % want,
                  " ".join(n for n in names if n))

        # val is advertised as a set of values, so it is offered as choices -
        # a typo in a field would be refused by the board after the fact.
        val_ctl = page.locator('#panelbody [data-param="val"] input[type=radio]')
        check(val_ctl.count() >= 2,
              "val is a set of choices, because the board declares it as one",
              "%d choices" % val_ctl.count())
        # ga is NOT: an address is neither a range nor a set, so it must stay
        # typeable. A vertical bar in its limits line would have made the panel
        # offer the format words as choices instead of an address field.
        ga_sel = page.locator('.card .row label[data-param="ga"]').locator('select')
        check(ga_sel.count() == 0, "ga stays typeable - an address is not a menu")

        page.fill("#raw", "pt.start knx mode=frames period=1000")
        page.click("#send")
        # A frame goes out once a period, comes back over the bus, and the peer
        # acknowledges it. Twenty-five seconds is room for several.
        #
        # ⚠️ Waited for on the rendered node, NOT on document.body.textContent.
        # That includes the <script> source, and this page's own source carries
        # the words 报文事件 and crc=raw inside a hint string - so a wait on the
        # body returns instantly, before a single frame has arrived, and every
        # assertion after it reads the page's source instead of its render.
        # That is exactly what happened while writing this check.
        box = page.locator(".card .chvals", has_text="报文事件")
        try:
            box.first.wait_for(timeout=25000)
        except Exception as e:
            Fail("no frame events on the knx card: %s" % type(e).__name__)
            failures.append("knx frame events missing")
        else:
            Ok("PASS  received frames appear on the card as events")
            text = box.first.inner_text()
            seen = " ".join(text.split())[:180]
            check("原样就对" in text or "取反才对" in text,
                  "the card says which reading of the octets passed the check octet",
                  seen)
            check("单字节应答" in text,
                  "a lone acknowledge octet reads as ack, not as a bad frame", seen)
            check("目标组地址 31/7/255" in text,
                  "and which group address the frame carried", seen)
        page.fill("#raw", "pt.stop knx")
        page.click("#send")
        page.wait_for_timeout(400)

    # ---------------------------------------------------- disconnect
    Section("disconnecting")
    page.locator("#disconnect").click()
    page.wait_for_selector("#afterconn:not(.live)")
    check(True, "disconnecting gates the page again")


if __name__ == "__main__":
    sys.exit(main())
