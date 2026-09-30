"""Uses the panel the way a person at the bench does, port by port, case by case.

    python naive.py --port COM5                 every port, spot checks only
    python naive.py --port COM5 --only rs485    one port
    python naive.py --port COM5 --show          watch it happen
    python naive.py --port COM5 --peer rs485=COM16     bind an adapter by hand
    python naive.py --port COM5 --long          also the long-running cases

Different question from run.py (case T4-02). H5 asks "does this control do what its
code says". This asks "does somebody who opens a port and presses the buttons
in the order they are printed get a sensible answer" - and it answers it by
pressing them, in that order, and reading what comes back.

That is not a re-run of H5 in a costume. Three bugs found on 2026-09-14 were
invisible to H5 because H5 knew where to click: a button that ran the wrong
half of a port, a card whose peer picker sat below the button that needed it,
and a verdict that showed the previous case's result. All three are only
visible if you follow the page instead of the code.

What it deliberately does NOT do:
  * 持续 - a burn-in runs for hours; this presses 单次 only
  * the analog multi-point walk - it stops to ask a person for a meter reading
  * the long-running storage cases - see LONG below

*** LONG: it skips the exhaustive storage cases by default. *** Sweeping all
64 MB of SDRAM and running the SD card's 64 stress passes proves the parts, not
the panel, and this script is about the panel. They also dominate its runtime.
A spot check answers the question this script asks - does pressing the button
give a sensible answer - and the exhaustive ones stay one flag away for whoever
actually wants to qualify a part. User 2026-09-14: 「不用 sd card sdram 你需要
简单跑一下，能用就行，全量的 sdram 和 card 让用户自己跑，你就随机抽查测试几处
就行」.

Needs a board. Exit 0 = nothing on any page looked wrong, 1 = something did,
2 = could not run at all.
"""

import argparse
import os
import sys
import time
from pathlib import Path

try:
    sys.stdout.reconfigure(line_buffering=True)
except AttributeError:
    pass

HERE = Path(__file__).resolve().parent
sys.path.insert(0, str(HERE))
sys.path.insert(0, str(HERE.parent.parent / "tools"))
import tool_repo  # noqa: E402,F401  - finds IAPTranfer_Tool's common.py

from common import Section, Ok, Fail, Warn  # noqa: E402
import run as h5  # noqa: E402  - the panel launcher and the process cleanup

problems = []


def bad(port, what, detail=""):
    problems.append((port, what, detail))
    Fail("  %-7s %s%s" % (port, what, ("  --  " + detail) if detail else ""))


def good(port, what):
    Ok("  %-7s %s" % (port, what))


# How long one case may take before it is called stuck. The slow ones are slow
# for a reason worth reporting, so they get room rather than a blanket number.
BUDGET_MS = {
    "sd.integrity": 120000,
    "sd.speed": 120000,
    "sdram.sweep": 300000,
    "sdram.crc": 60000,
}
DEFAULT_BUDGET_MS = 60000

# Exhaustive by nature: 64 stress passes over the card, the whole 64 MB array.
# They qualify the part; this script checks the page. Skipped unless --long.
LONG_CASES = ("sdram.sweep",)

# Ports that wear out. Each press is a real mechanical cycle from a finite
# supply (HF41F: 30k), so these get the plan's mode once and no mode sweep.
WEARS_OUT = {"relay"}


def flat(node):
    return " ".join(node.inner_text().split())


def y_of(node):
    b = node.bounding_box()
    return b["y"] if b else -1


def audit_layout(page, port, card):
    """Everything that is about the shape of the page, not about the board."""
    cases = card.locator(".case")
    n = cases.count()
    if n == 0:
        bad(port, "卡片上一个用例都没有")
        return 0

    # Every case has to say what it tests and how. A case with a button and no
    # explanation is the state this whole layout exists to end.
    for i in range(n):
        c = cases.nth(i)
        t = flat(c)
        title = c.locator(".casehd .nm")
        if not title.count() or not title.first.inner_text().strip():
            bad(port, "第 %d 个用例没有标题" % (i + 1))
        for label in ("测什么", "怎么做"):
            if label not in t:
                bad(port, "第 %d 个用例缺「%s」" % (i + 1, label))
        if "什么算过" not in t:
            # Not every case can have a criterion - say which, do not fail it.
            Warn("  %-7s 第 %d 个用例没写「什么算过」" % (port, i + 1))

        # The order the whole redesign is about: config, then the button, then
        # the result.
        act = c.locator(".caseact")
        res = c.locator(".caseres")
        cfg = c.locator(".casecfg")
        if act.count() and res.count() and y_of(act.first) > y_of(res.first):
            bad(port, "第 %d 个用例的按钮排在结果下面" % (i + 1))
        if cfg.count() and act.count() and y_of(cfg.first) > y_of(act.first):
            bad(port, "第 %d 个用例的配置排在按钮下面" % (i + 1))

        # A named choice that does not say what it is for is a dropdown with
        # extra steps. The whole reason these are radios instead of a <select>
        # is that each value gets a sentence beside it - CAN's 回显 sat there
        # bare on 2026-09-14 because the firmware gained a mode and the help
        # table did not.
        opts = c.locator(".enumpick label")
        for j in range(opts.count()):
            txt = flat(opts.nth(j))
            if "——" not in txt:
                bad(port, "选项「%s」后面没写它是干什么的" % txt[:16])

        # One button per case. Helpers (ping, 全都设成第一路的值) live in the
        # config area, so the action area is where "how many do I press" is
        # answered.
        btns = act.locator("button")
        primary = [j for j in range(btns.count())
                   if "primary" in (btns.nth(j).get_attribute("class") or "")]
        if len(primary) > 1:
            bad(port, "第 %d 个用例有 %d 个主按钮" % (i + 1, len(primary)))

    # Where the peer picker belongs depends on who does the work.
    #
    # An adapter the person has to plug in is a prerequisite, so it sits above
    # every case - nothing on the card can be done before it. One the panel
    # connects by itself is just a setting of the case that uses it, and it
    # cannot be done first anyway: the board only enumerates its CDC port once
    # the session is running.
    peer = card.locator(".peerbox")
    for j in range(peer.count()):
        kind = peer.nth(j).get_attribute("data-peer") or "com"
        inside = peer.nth(j).evaluate("e => !!e.closest('.case')")
        if kind == "com":
            if inside or y_of(peer.nth(j)) > y_of(cases.first):
                bad(port, "要人插的对端排在用例里/用例后面")
            else:
                good(port, "要人插的对端排在最前面")
        else:
            if not inside:
                bad(port, "面板自己接的对端却排在所有用例之前")
            else:
                good(port, "面板自己接的对端排在它那一段的配置里")

    # A port with one case must not offer a second button that runs "all of
    # them" - there is nothing to distinguish it from.
    allrow = card.locator(".allrow")
    if n == 1 and allrow.count():
        bad(port, "只有一个用例，却还有「依次跑一遍」")
    if n > 1 and not allrow.count():
        bad(port, "有 %d 个用例，却没有「依次跑一遍」" % n)
    if n > 1 and allrow.count() and y_of(allrow.first) < y_of(cases.nth(n - 1)):
        bad(port, "「依次跑一遍」排在用例中间")

    # Nothing on the card may still speak protocol at the reader.
    t = flat(card)
    for leak in ("loop=ctrl", "loop=link", "kind=session", "undefined", "[object"):
        if leak in t:
            bad(port, "卡片上出现了 %s" % leak)
    return n


def bind_peer(page, port, com):
    """Binds the adapter the way the person at the bench would."""
    box = page.locator('.card[data-port="%s"] .peerbox' % port)
    if not box.count():
        return False
    if "解开" in flat(box.first):
        return True
    sel = box.first.locator("select")
    if not sel.count():
        return False
    opts = sel.first.locator("option")
    for i in range(opts.count()):
        if com in (opts.nth(i).get_attribute("value") or ""):
            sel.first.select_option(opts.nth(i).get_attribute("value"))
            box.first.locator("button").first.click()
            page.wait_for_timeout(700)
            return "解开" in flat(page.locator('.card[data-port="%s"] .peerbox' % port).first)
    return False


def verdict_of(card, key):
    n = card.locator('[data-case="%s"]' % key)
    return flat(n.first) if n.count() else ""


def mode_values(case):
    """Which session modes this case offers, in the order they are printed.

    Only mode= - the other enums (a baud rate, a loop kind) do not change what
    the far end of the link has to do, and that is what this is for.
    """
    rs = case.locator(".enumpick input[type=radio]")
    out = []
    for j in range(rs.count()):
        if (rs.nth(j).get_attribute("name") or "").endswith("-mode"):
            v = rs.nth(j).get_attribute("value")
            if v and v not in out:
                out.append(v)
    return out


def peer_trouble(page, port):
    """Whether the peer broke - which is not the same as the board failing.

    A card can report a clean red verdict while the reason is that this end
    stopped reading. On 2026-09-14 sink read zero forever and source killed the
    peer outright, and the board's own numbers were the only thing anyone
    looked at.
    """
    box = page.locator('.card[data-port="%s"] .peerbox' % port)
    for j in range(box.count()):
        t = flat(box.nth(j))
        if "出问题了" in t:
            return t
    return ""


def press_case(page, port, card, i, results, long_ok):
    """Presses one case, once per mode it offers.

    *** Every mode, not just the one the plan ships. *** The plan runs usb and
    eth with mode=echo, so a sweep that only presses the default never touches
    sink or source - and those are on the page, with a sentence beside each
    saying what they do. A button a person can press is a button that has to
    work.
    """
    case = card.locator(".case").nth(i)
    modes = mode_values(case)
    if not modes or port in WEARS_OUT:
        # Only the mode the plan ships, once. Every press of a relay is a real
        # mechanical cycle out of a finite supply, and this sweep is here to
        # prove the button works, not to qualify the part. User 2026-09-15:
        # 「relay 的寿命，响几下通了没问题了，就跳过，不要一直测」.
        press_once(page, port, card, i, results, long_ok, None)
        return
    for m in modes:
        # Stop whatever the last mode left running before starting the next.
        # A session holds its resources until it is told to stop - the board
        # answered the second mode with "port 5000 is already bound", which
        # reads exactly like a broken mode and is nothing of the kind.
        stop = page.locator("#panelbody button[data-stop]")
        if stop.count():
            stop.first.click()
            page.wait_for_timeout(800)
        card = page.locator('.card[data-port="%s"]' % port)
        if not card.count():
            return
        press_once(page, port, card, i, results, long_ok, m)


def press_once(page, port, card, i, results, long_ok, mode):
    """Presses one case's button and waits for its own result to change."""
    case = card.locator(".case").nth(i)
    title = flat(case.locator(".casehd .nm").first)
    if mode:
        r = case.locator('.enumpick input[type=radio][value="%s"]' % mode)
        if not r.count():
            bad(port, "「%s」找不到模式 %s 的单选" % (title, mode))
            return
        r.first.check()
        # Picking a radio rebuilds the card, so every locator above is stale.
        page.wait_for_timeout(250)
        card = page.locator('.card[data-port="%s"]' % port)
        if not card.count():
            bad(port, "选了模式 %s 之后卡片没了" % mode)
            return
        case = card.locator(".case").nth(i)
        title = "%s · %s" % (flat(case.locator(".casehd .nm").first), mode)

    act = case.locator(".caseact")
    if not act.count():
        bad(port, "第 %d 个用例没有执行按钮" % (i + 1))
        return

    btn = act.locator("button.primary")
    if not btn.count():
        bad(port, "「%s」没有主按钮" % title)
        return

    key = act.locator("button[data-run]").first.get_attribute("data-run") \
        if act.locator("button[data-run]").count() else "session"
    # Said out loud rather than silently passed over: a skipped case that reads
    # like a green one is how a sweep comes to mean less than it claims.
    if key in LONG_CASES and not long_ok:
        Warn("  %-7s 「%s」跳过 —— 全量的那几项由人自己跑（加 --long 才跑）" % (port, title))
        results.append((port, key, title, "跳过（加 --long 才跑）", 0.0))
        return

    before = verdict_of(card, key)
    budget = BUDGET_MS.get(key, DEFAULT_BUDGET_MS)

    t0 = time.time()
    btn.first.click()

    # Wait on this case's own result, not on the port's status: a port with
    # five cases has one status and five answers, and reading the status is how
    # a passing case gets reported as the previous case's failure.
    deadline = time.time() + budget / 1000.0
    now = before
    started = False
    while time.time() < deadline:
        page.wait_for_timeout(400)
        card = page.locator('.card[data-port="%s"]' % port)
        if not card.count():
            break
        now = verdict_of(card, key)
        busy = page.locator('#panelbody button:has-text("跑着")').count()
        if busy:
            # Once it is seen running, the run ending is the signal. The same
            # case pressed a second time in another mode can land on the very
            # same words, and waiting for the text to differ would sit here
            # until the budget ran out on a case that finished in a second.
            started = True
            continue
        if now and now != "还没跑" and (started or now != before):
            break
    took = time.time() - t0

    card = page.locator('.card[data-port="%s"]' % port)
    now = verdict_of(card, key) if card.count() else now
    if not now or now == "还没跑":
        bad(port, "「%s」按下去 %.0f 秒还是没有结果" % (title, took))
        results.append((port, key, title, "没反应", took))
        return
    state = "过" if now.startswith("✅") else "不过" if now.startswith("❌") else "无判据"
    results.append((port, key, title, now, took))
    if state == "过":
        good(port, "「%s」%s（%.1f 秒）" % (title, now, took))
    else:
        Warn("  %-7s 「%s」%s（%.1f 秒）" % (port, title, now, took))

    # A red verdict can be the board; a broken peer is always this software.
    trouble = peer_trouble(page, port)
    if trouble:
        bad(port, "跑「%s」之后对端自己坏了：%s" % (title, trouble[:70]))


def walk(page, com, only, peers, long_ok):
    page.locator('.tab[data-tab="manual"]').click()
    page.wait_for_selector(".prow")

    rows = page.locator(".prow")
    names = [rows.nth(i).get_attribute("data-port") for i in range(rows.count())]
    names = [n for n in names if n]
    if only:
        names = [n for n in names if n in only]
    print("  要走的端口：%s" % " ".join(names))

    results = []
    for name in names:
        Section("端口 %s" % name)
        page.locator('.prow[data-port="%s"]' % name).first.click()
        page.wait_for_timeout(500)
        card = page.locator('.card[data-port="%s"]' % name)
        if not card.count():
            bad(name, "点了端口，卡片没出来")
            continue
        n = audit_layout(page, name, card.first)
        if n == 0:
            continue

        # An adapter the panel cannot identify has to be chosen by hand - so
        # choose it by hand, the way the card says to. Only for the ports the
        # caller named: binding a CH340 to CAN because both cards have a picker
        # would be the panel's old guessing bug, reproduced in the test.
        peer_com = peers.get(name)
        if peer_com and card.first.locator(".peerbox").count():
            if bind_peer(page, name, peer_com):
                good(name, "绑上了对端 %s" % peer_com)
            else:
                bad(name, "没能绑上对端 %s" % peer_com)

        for i in range(n):
            card = page.locator('.card[data-port="%s"]' % name)
            if not card.count():
                break
            press_case(page, name, card.first, i, results, long_ok)

        # A card that hangs under a port rather than being one of its cases -
        # today only the analog multi-point walk - still has to read like
        # everything else on the page. Only its shape is checked: it stops to
        # ask a person for a meter reading, so pressing it unattended would
        # report a missing instrument as a broken board.
        extra = page.locator(".card[data-extra]")
        for j in range(extra.count()):
            audit_layout(page, name + "+" + (extra.nth(j).get_attribute("data-extra") or "?"),
                         extra.nth(j))

        # Leave nothing running: the next port's frames would arrive mixed in
        # with this one's, and an output left driving is a hazard, not a state.
        stop = page.locator('#panelbody button[data-stop]')
        if stop.count():
            stop.first.click()
            page.wait_for_timeout(600)
    return results


def walk_aocal(page):
    """The analog multi-point card: every control, without inventing readings.

    *** It does not type a meter reading. *** The numbers this card produces are
    a calibration of a real board, and one made up at a keyboard would look
    exactly like a measured one in the report. So this presses 跳过这一点 for
    every point and then 中止 on a second pass: that exercises the button, the
    prompt, the skip path and the abort path, and leaves the verdict to whoever
    is holding the meter.
    """
    port = "aout+读表"
    card = page.locator('.card[data-port="aout"]')
    if not card.count():
        bad(port, "找不到 AO 卡片")
        return

    # 一张卡两种用法，靠「读数来自」分开（DECISIONS 47）。不选人工读表，
    # 这就是产线方案无人值守跑的那一档。
    pick = card.locator('input[type=radio][data-meter="manual"]')
    if not pick.count():
        bad(port, "AO 卡上没有「读数来自」这个选项")
        return
    if not card.locator('input[type=radio][data-meter="none"]').count():
        bad(port, "「不读表」那一档不见了 —— 方案跑的就是它")
    pick.first.check()
    page.wait_for_timeout(400)
    card = page.locator('.card[data-port="aout"]')
    good(port, "选了「人工读表」")

    for label in ("测哪几个点", "合格判据", "测哪一路"):
        if label not in flat(card):
            bad(port, "选了读表之后少了「%s」" % label)

    go = card.locator("button.aocalgo")
    if not go.count():
        bad(port, "没有「走一遍」按钮")
        return

    # Pass one: skip every point. Too few points is a real edge - the fit has
    # to refuse rather than invent a line through one reading.
    go.first.click()
    # Each point sets the DAC and waits for two fresh frames, so the next
    # prompt is a second or two behind the last one. Waiting on the button
    # coming back rather than on the gap is what tells "finished" from
    # "still between points".
    skipped = 0
    deadline = time.time() + 90
    while time.time() < deadline:
        page.wait_for_timeout(400)
        if page.locator("#meterask:not([hidden])").count():
            page.locator("#meterskip").click()
            skipped += 1
            continue
        if skipped and page.locator(
                '.card[data-extra="aocal"] button.aocalgo:not([disabled])').count():
            break
    if not skipped:
        bad(port, "点了「走一遍」，没有弹出过读数输入框")
        return
    good(port, "「走一遍」逐点停下来问读数，%d 个点都能跳过" % skipped)

    page.wait_for_timeout(1500)
    said = flat(card)
    if "没有写进板子" not in said:
        bad(port, "卡片没说清系数不会写进板子")
    else:
        good(port, "卡片说明了系数不写进板子")

    # Pass two: abort partway. A run nobody can stop is a run that owns the
    # board until it finishes.
    #
    # First the button has to come back at all: a card left saying 跑着… after
    # every point was skipped can never be run a second time.
    ready = False
    for _ in range(20):
        page.wait_for_timeout(500)
        if page.locator('.card[data-port="aout"] button.aocalgo:not([disabled])').count():
            ready = True
            break
    if not ready:
        bad(port, "跳过所有点之后按钮一直停在「跑着…」，这张卡跑不了第二轮")
        return
    good(port, "跳完所有点之后按钮恢复可用")
    go = page.locator('.card[data-port="aout"] button.aocalgo')
    go.first.click()
    for _ in range(10):
        page.wait_for_timeout(500)
        if page.locator("#meterask:not([hidden])").count():
            page.locator("#meterstop").click()
            break
    page.wait_for_timeout(1200)
    if page.locator("#meterask:not([hidden])").count():
        bad(port, "点了「中止」，读数输入框还在")
    else:
        good(port, "「中止」把这一轮停下来了")
    if page.locator('.card[data-port="aout"] button.aocalgo[disabled]').count():
        bad(port, "中止之后「走一遍」还是灰的，跑不了第二轮")


def main():
    ap = argparse.ArgumentParser(description=__doc__)
    ap.add_argument("--port", required=True, help="the board's RS232 control port")
    ap.add_argument("--only", help="comma-separated port names, default all")
    # port=COM pairs, not one COM for everything. The panel cannot tell which
    # adapter is wired to which terminal and neither can this script - only the
    # person at the bench can, so they say it, one port at a time.
    ap.add_argument("--peer", default="",
                    help="which adapter is on which port, e.g. rs485=COM16")
    ap.add_argument("--aocal", action="store_true",
                    help="also walk the analog multi-point card (no meter needed)")
    ap.add_argument("--long", action="store_true",
                    help="also run the exhaustive storage cases (see LONG above)")
    ap.add_argument("--show", action="store_true", help="visible browser window")
    args = ap.parse_args()

    try:
        from playwright.sync_api import sync_playwright
    except ImportError:
        Fail("playwright is not installed - python -m pip install playwright")
        return 2

    exe = h5.panel_exe()
    only = [s.strip() for s in (args.only or "").split(",") if s.strip()]
    peers = {}
    for pair in args.peer.split(","):
        if "=" in pair:
            k, v = pair.split("=", 1)
            peers[k.strip()] = v.strip()

    browsers_before = h5.pids_of(h5.BROWSER_IMAGES)
    Section("面板")
    proc, base, log = h5.start_panel(exe)
    print("  %s   控制口 %s" % (base, args.port))
    print("  面板日志 %s" % log)

    results = []
    try:
        with sync_playwright() as pw:
            browser = pw.chromium.launch(channel="chrome", headless=not args.show)
            page = browser.new_page()
            page.set_default_timeout(15000)
            h5.watch_for_page_errors(page)
            page.goto(base)

            page.wait_for_selector("#portlist .p")
            rows = page.locator("#portlist .p")
            hit = None
            for i in range(rows.count()):
                if args.port in rows.nth(i).inner_text():
                    hit = rows.nth(i)
                    break
            if hit is None:
                Fail("控制口 %s 不在列表里" % args.port)
                return 2
            hit.click()
            page.locator("#connect").click()
            page.wait_for_selector("#afterconn.live")
            page.wait_for_timeout(1200)

            results = walk(page, args.port, only, peers, args.long)
            if args.aocal:
                Section("模拟输出多点测量")
                walk_aocal(page)
            page.goto("about:blank")
    finally:
        h5.stop_panel(proc)
        h5.kill_new(h5.BROWSER_IMAGES, browsers_before)

    Section("每个用例跑出来什么")
    for port, key, title, note, took in results:
        print("  %-7s %-22s %-34s %5.1fs  %s" % (port, key, title[:34], took, note))

    Section("结论")
    if problems:
        Fail("%d 处页面问题" % len(problems))
        for port, what, detail in problems:
            print("    %-7s %s%s" % (port, what, ("  --  " + detail) if detail else ""))
        code = 1
    else:
        Ok("每个端口的页面都说得通")
        code = 0

    sys.stdout.flush()
    os._exit(code)


if __name__ == "__main__":
    sys.exit(main())
