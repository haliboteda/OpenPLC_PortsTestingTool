"""Writes config/machine.py: the paths this repo's scripts need on this machine.

    python tools/init_machine.py                 detect and write
    python tools/init_machine.py --set KEY=VALUE  set one value outright
    python tools/init_machine.py --check          report, write nothing

A value already in machine.py is kept while it still exists on disk; only
missing or stale ones are detected again. machine.py is gitignored: the record
of what belongs in it is the SETTINGS table below, not a template.

It cannot import common.py, because common.py exits when machine.py is
missing -- which is exactly when this script runs.

Exit 0 = every required setting resolved, 1 = something required is missing.
"""

import argparse
import glob
import os
import re
import shutil
import subprocess
import sys
from pathlib import Path

IS_WIN = sys.platform.startswith("win")
PLATFORM = "windows" if IS_WIN else ("macos" if sys.platform == "darwin" else "linux")
HOME = Path.home()
HERE = Path(__file__).resolve().parent
REPO = HERE.parent.parent
CONFIG = HERE.parent / "config" / "machine.py"


def newest(pattern):
    hits = sorted(glob.glob(str(pattern)))
    return hits[-1] if hits else None


# ---------------------------------------------------------------- detectors
def detect_repo(name):
    """Repos sit next to each other; one level further out is the fallback."""
    for base in (REPO.parent, REPO.parent.parent):
        for pat in (name, "*/" + name):
            hit = newest(base / pat)
            if hit and (Path(hit) / ".git").exists():
                return hit
    return None


def cubeide_roots():
    if IS_WIN:
        return [r"C:\ST\STM32CubeIDE*", r"D:\ST\STM32CubeIDE*", r"E:\ST\STM32CubeIDE*",
                r"C:\Program Files\STMicroelectronics\STM32CubeIDE*",
                str(Path(os.environ.get("LOCALAPPDATA", "x")) / "Programs" / "STM32CubeIDE*")]
    if PLATFORM == "macos":
        return ["/Applications/STM32CubeIDE*", str(HOME / "Applications" / "STM32CubeIDE*")]
    return ["/opt/st/stm32cubeide*", "/opt/stm32cubeide*", str(HOME / "st" / "stm32cubeide*")]


def detect_cubeide():
    for r in cubeide_roots():
        for hit in sorted(glob.glob(r)):
            # The install root is the directory holding STM32CubeIDE/plugins.
            if (Path(hit) / "STM32CubeIDE" / "plugins").is_dir():
                return hit
    return None


def gcc_is_modern(exe):
    """GCC older than 5 (Dev-C++ ships 3.4.2) rejects -std=c11: not a usable find."""
    try:
        out = subprocess.run([exe, "--version"], capture_output=True, text=True,
                             timeout=20).stdout
    except Exception:
        return False
    m = re.search(r"(\d+)\.(\d+)\.(\d+)", out)
    return bool(m) and int(m.group(1)) >= 5


def detect_host_cc():
    cands = [shutil.which("gcc") or shutil.which("clang")]
    if IS_WIN:
        cands += [newest(r"C:\mingw64\bin\gcc.exe"), newest(r"D:\Soft\mingw64\bin\gcc.exe"),
                  newest(r"C:\msys64\mingw64\bin\gcc.exe")]
    for c in cands:
        if c and gcc_is_modern(c):
            return c
    return None


def detect_git_bash():
    """A real bash for compile_tool.sh. On Windows NOT the bash on PATH, which is
    the WSL launcher; derived from git so a non-default install is followed."""
    if not IS_WIN:
        return shutil.which("bash")
    git = shutil.which("git")
    roots = [Path(git).resolve().parent.parent] if git else []
    roots += [Path(r) for r in (r"C:\Program Files\Git", r"D:\Program Files\Git")]
    for root in roots:
        for rel in ("usr/bin/bash.exe", "bin/bash.exe"):
            if (root / rel).exists():
                return str(root / rel)
    return None


# (key, detector, required, comment)
SETTINGS = [
    ("BOOT_REPO", lambda r: detect_repo("open_plc_cube_ide"), True,
     "open_plc_cube_ide: the fixture firmware (TestCase/porttool/) and the CubeIDE project"),
    ("DOCS_REPO", lambda r: detect_repo("OpenPLC_Docs"), False,
     "OpenPLC_Docs: DOCS checks that every $PORTTOOL/... path it names exists"),
    ("CUBEIDE", lambda r: detect_cubeide(), False,
     "STM32CubeIDE install root: headless fixture build and STM32_Programmer_CLI"),
    ("WORKSPACE", lambda r: str(Path(r["BOOT_REPO"]).parent) if r.get("BOOT_REPO") else None, False,
     "Eclipse workspace holding the bootloader project"),
    ("HOST_CC", lambda r: detect_host_cc(), False,
     "a modern gcc/clang (GCC 5+) for the host build of the fixture firmware (T4-01)"),
    ("GIT_BASH", lambda r: detect_git_bash(), False,
     "a real bash for compile_tool.sh"),
]


def load_existing():
    if not CONFIG.exists():
        return {}
    ns = {}
    exec(CONFIG.read_text(encoding="utf-8"), ns)
    return {k: ns[k] for k, *_ in SETTINGS if k in ns}


def main():
    ap = argparse.ArgumentParser(description=__doc__,
                                 formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("--set", action="append", default=[], metavar="KEY=VALUE")
    ap.add_argument("--check", action="store_true")
    args = ap.parse_args()

    keys = [k for k, *_ in SETTINGS]
    forced = {}
    for kv in args.set:
        k, _, v = kv.partition("=")
        if k not in keys:
            print("unknown setting %s (known: %s)" % (k, ", ".join(keys)))
            return 1
        forced[k] = v

    old = load_existing()
    resolved, missing = {}, []
    for key, detect, required, _ in SETTINGS:
        if key in forced:
            val = forced[key]
        elif old.get(key) and Path(old[key]).exists():
            val = old[key]
        else:
            val = detect(resolved) or ""
        resolved[key] = val
        state = "ok     " if val else ("MISSING" if required else "empty  ")
        print("  %s %-10s %s" % (state, key, val))
        if required and not val:
            missing.append(key)

    if args.check:
        return 1 if missing else 0

    lines = ['"""Machine-local paths. Generated by tools/init_machine.py; gitignored."""', ""]
    for key, _, _, comment in SETTINGS:
        lines.append("# " + comment)
        lines.append("%s = r\"%s\"" % (key, resolved[key]))
    CONFIG.parent.mkdir(parents=True, exist_ok=True)
    CONFIG.write_text("\n".join(lines) + "\n", encoding="utf-8")
    print("wrote %s" % CONFIG)
    if missing:
        print("missing: %s -- set with --set KEY=VALUE" % ", ".join(missing))
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
