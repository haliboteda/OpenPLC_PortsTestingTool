"""Shared by the scripts of this repo. Import it first:

    from common import cfg, Section, Ok, Warn, Fail, EXE, ...

Machine-local paths come from config/machine.py, which tools/init_machine.py
generates; nothing else in this repo spells out a path on this machine.
"""

import glob
import os
import sys
from pathlib import Path

# ---------------------------------------------------------------- platform
if sys.platform.startswith("win"):
    PLATFORM = "windows"
elif sys.platform == "darwin":
    PLATFORM = "macos"
else:
    PLATFORM = "linux"

IS_WIN = PLATFORM == "windows"
EXE = ".exe" if IS_WIN else ""
# CubeIDE's externaltools plugin suffix for this platform.
CUBE_PLUG = {"windows": "win32", "linux": "linux64", "macos": "macos64"}[PLATFORM]


# ---------------------------------------------------------------- config
def _load_machine():
    """Import config/machine.py, the one file that differs between machines."""
    path = Path(__file__).resolve().parent.parent / "config" / "machine.py"
    if not path.exists():
        print("config/machine.py is missing. Generate it -- this machine's paths "
              "are detected, not typed:", file=sys.stderr)
        print("    python tools/init_machine.py", file=sys.stderr)
        sys.exit(1)
    import importlib.util
    spec = importlib.util.spec_from_file_location("machine", path)
    mod = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(mod)
    return mod


cfg = _load_machine()


# ---------------------------------------------------------------- output
def _colour_ok():
    if os.environ.get("NO_COLOR") or not sys.stdout.isatty():
        return False
    if IS_WIN:
        try:
            import ctypes
            k = ctypes.windll.kernel32
            k.SetConsoleMode(k.GetStdHandle(-11), 7)
        except Exception:
            return False
    return True


_COLOUR = _colour_ok()

# A Windows console on a legacy codepage cannot encode the Chinese and the
# warning signs in this output; replace rather than crash.
try:
    sys.stdout.reconfigure(errors="replace")
    sys.stderr.reconfigure(errors="replace")
except (AttributeError, ValueError):
    pass


def _paint(text, code):
    return "\033[%sm%s\033[0m" % (code, text) if _COLOUR else text


def _emit(text):
    try:
        print(text)
    except UnicodeEncodeError:
        enc = sys.stdout.encoding or "ascii"
        print(text.encode(enc, "replace").decode(enc, "replace"))


def Section(t): _emit(""); _emit(_paint("===== " + t, "36"))
def Ok(t):      _emit(_paint(t, "32"))
def Warn(t):    _emit(_paint(t, "33"))
def Fail(t):    _emit(_paint(t, "31"))


# ---------------------------------------------------------------- files
def read_text(path):
    """A whole file, raw: CRLF kept, a UTF-8 BOM stripped (it breaks ^ anchors)."""
    with open(str(path), "r", encoding="utf-8", errors="replace", newline="") as fh:
        text = fh.read()
    return text[1:] if text.startswith("﻿") else text


# ---------------------------------------------------------------- toolchain
def _newest(pattern):
    hits = sorted(glob.glob(str(pattern)))
    return Path(hits[-1]) if hits else None


def get_programmer_cli():
    """STM32_Programmer_CLI inside CubeIDE's versioned plugin; newest wins.

    The non-Windows plugin suffixes are ST's documented naming, not verified here.
    """
    pattern = (Path(cfg.CUBEIDE) / "STM32CubeIDE" / "plugins"
               / ("com.st.stm32cube.ide.mcu.externaltools.cubeprogrammer.%s_*" % CUBE_PLUG)
               / "tools" / "bin" / ("STM32_Programmer_CLI" + EXE))
    hit = _newest(pattern)
    if not hit:
        Fail("STM32_Programmer_CLI not found. Looked for: %s" % pattern)
        sys.exit(1)
    return hit


def get_cube_ide_exe():
    """CubeIDE's headless launcher, whose name differs per platform."""
    name = "stm32cubeidec.exe" if IS_WIN else "stm32cubeide"
    exe = Path(cfg.CUBEIDE) / "STM32CubeIDE" / name
    if not exe.exists():
        Fail("%s not found at %s" % (name, exe))
        sys.exit(1)
    return exe
