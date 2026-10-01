"""Builds the fixture firmware and PortTool, flashes the board, packs the delivery.

    python build.py                    pick from a menu
    python build.py --fixture --tool   the fixture firmware and PortTool
    python build.py --fixture          just the fixture firmware
    python build.py --tool             just PortTool
    python build.py --flash            write Debug/ to the board over SWD
    python build.py --deliver          pack the folder for the hardware engineer

The flags are a set, not a choice, and combine in the order above.

⚠️ CubeIDE must be CLOSED for a firmware build: a headless build cannot take a
locked workspace.

⚠️ After a fixture build, $BOOT/Debug/ holds an image that is NOT a bootloader.

It hands off rather than reimplementing: the firmware build is
TestCase/tools/build_fixture.py, and compile_tool.sh owns the output layout and
the three platforms.

Exit 0 = everything asked for was built, 1 = a build failed, 2 = bad usage.
"""

import argparse
import shutil
import subprocess
import sys
from pathlib import Path

# A heading has to reach the terminal before the child process writes over it.
try:
    sys.stdout.reconfigure(line_buffering=True)
except AttributeError:
    pass

HERE = Path(__file__).resolve().parent
sys.path.insert(0, str(HERE / "TestCase" / "tools"))

from common import Fail, Ok, Section, Warn, cfg, get_programmer_cli  # noqa: E402

# (显示名, 备注, 编工装固件, 编 PortTool, 烧进板子, 打包交付)
MENU = [
    ("工装固件 + PortTool", "　(默认，日常上板调试)", True, True, False, False),
    ("只编工装固件", "", True, False, False, False),
    ("只编 PortTool", "", False, True, False, False),
    ("编工装固件 + 烧进板子", "　(接着 ST-Link)", True, False, True, False),
    ("只烧，不编", "　(用 Debug/ 里现成的)", False, False, True, False),
    ("做交付文件夹", "　(编工装固件 + PortTool，再打包给硬件工程师)", True, True, False, True),
]


def ask():
    """Returns (fixture, tool, flash, deliver) from the menu, or the default with no answer."""
    if sys.stdin.isatty():
        print("")
        print("  要编什么？")
        for i, (name, note, *_) in enumerate(MENU, 1):
            print("    %d) %s%s" % (i, name, note))
        print("")
        sys.stdout.write("  选 [1-%d，回车=1]: " % len(MENU))
        sys.stdout.flush()
    line = sys.stdin.readline()
    if line == "":
        # Nobody there to ask - a script, a CI job. Build the usual pair, and
        # never touch the board: flashing unasked is not a default.
        return MENU[0][2], MENU[0][3], False, False
    pick = line.strip() or "1"
    if not pick.isdigit() or not (1 <= int(pick) <= len(MENU)):
        Fail("没有这一项：" + pick)
        sys.exit(2)
    return MENU[int(pick) - 1][2:]


def build_fixture():
    Section("固件：工装")
    args = [sys.executable, str(HERE / "TestCase" / "tools" / "build_fixture.py")]
    return subprocess.call(args, cwd=str(HERE / "TestCase")) == 0


def rebuild_sim():
    """The simulated board compiles the same porttool sources as the fixture.

    *** So a firmware change leaves it stale, and T4-02 then tests yesterday's
    protocol against today's panel. *** Failing to build it is a warning, not an
    error: it needs a host compiler, and a machine without one can still
    build firmware and tools.
    """
    Section("模拟板")
    rc = subprocess.call([sys.executable, "build.py", "--sim"],
                         cwd=str(HERE / "TestCase" / "host" / "porttool_caps"))
    if rc != 0:
        Warn("模拟板没重建（多半是主机 gcc 没装）—— T4-02 会用旧的那个。")
    return rc == 0


def flash_debug():
    """Writes Debug/ to the board over SWD - whatever image is there."""
    Section("烧进板子")
    elf = Path(cfg.BOOT_REPO) / "Debug" / "open_plc_cube_ide.elf"
    if not elf.exists():
        Fail("Debug/ 里没有镜像 —— 先编一个。")
        return False
    try:
        cli = get_programmer_cli()
    except SystemExit:
        return False
    print("  " + str(elf))
    rc = subprocess.call([cli, "-c", "port=SWD", "mode=UR",
                          "-d", str(elf), "-v", "-rst"])
    if rc != 0:
        Fail("烧不进去 —— 看上面。ST-Link 插了吗？板子上电了吗？")
    return rc == 0


def find_bash():
    """The bash that can run compile_tool.sh.

    ⚠️ On Windows, `bash` on PATH is System32\\bash.exe - the WSL launcher, not
    a shell. config/machine.py names the real one.
    """
    named = getattr(cfg, "GIT_BASH", "")
    if named and Path(named).exists():
        return named
    found = shutil.which("bash")
    if found and "system32" in found.lower():
        return None      # WSL, not a shell
    return found


def build_tool():
    Section("PortTool")
    bash = find_bash()
    if bash is None:
        Fail("找不到能跑 compile_tool.sh 的 bash。Windows 上 PATH 里那个是 WSL 的启动器，"
             "不是 shell。在 TestCase 里跑一遍：python tools/init_machine.py "
             "—— 它会把 Git Bash 的位置写进 config/machine.py。")
        return False
    return subprocess.call([bash, str(HERE / "compile_tool.sh")], cwd=str(HERE)) == 0


def main():
    ap = argparse.ArgumentParser(
        description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("--fixture", action="store_true", help="工装固件")
    ap.add_argument("--tool", action="store_true", help="PortTool")
    ap.add_argument("--flash", action="store_true", help="把 Debug/ 里的镜像烧进板子")
    ap.add_argument("--deliver", action="store_true", help="打包交付文件夹")
    args = ap.parse_args()

    if args.fixture or args.tool or args.flash or args.deliver:
        fixture, tool, flash, deliver = args.fixture, args.tool, args.flash, args.deliver
    else:
        fixture, tool, flash, deliver = ask()

    if fixture:
        print("")
        Warn("编固件前 CubeIDE 要关掉 —— headless 构建拿不到被锁住的 workspace。")

    ok = True
    if fixture:
        ok = build_fixture() and ok
        if ok:
            rebuild_sim()
    if tool:
        ok = build_tool() and ok
    # Only onto a board that got what was just built: flashing after a failed
    # build would put the previous image back without saying so.
    if flash and ok:
        ok = flash_debug() and ok
    if deliver and ok:
        ok = subprocess.call(
            [sys.executable, str(HERE / "TestCase" / "tools" / "make_delivery.py")],
            cwd=str(HERE / "TestCase")) == 0 and ok

    Section("结果")
    if not ok:
        Fail("有东西没编过 —— 看上面。")
        return 1
    did = (["工装固件"] if fixture else []) + (["PortTool"] if tool else [])
    Ok(("编好了：" + " + ".join(did) if did else "做完了") +
       ("，已烧进板子" if flash else "") +
       ("，交付文件夹已更新" if deliver else ""))
    if fixture:
        Warn("$BOOT/Debug/ 里现在是工装镜像，不是 bootloader。")
    return 0


if __name__ == "__main__":
    sys.exit(main())
