"""Used only by the temporary AO diagnostic aodiag.py (STG-41); goes with it.

    from common import cfg, Section, Ok, Warn, Fail, EXE, ...

Machine-local paths come from tools/local.json, which `tools/build.sh find-tools`
writes; nothing else in this repo spells out a path on this machine.
"""

import json
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


# ---------------------------------------------------------------- config
class _Local:
    """tools/local.json as attributes; a key it lacks reads as ""."""

    def __init__(self, values):
        self.__dict__.update(values)

    def __getattr__(self, name):
        return ""


def _load_local():
    path = Path(__file__).resolve().parents[2] / "tools" / "local.json"
    if not path.exists():
        print("tools/local.json is missing. Generate it -- this machine's paths "
              "are detected, not typed:", file=sys.stderr)
        print("    tools/build.sh find-tools", file=sys.stderr)
        sys.exit(1)
    return _Local(json.loads(path.read_text(encoding="utf-8")))


cfg = _load_local()


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


# ---------------------------------------------------------------- repos
def core_repo():
    """The board package repo: $CORE_REPO, else CORE_REPO in local.json."""
    return Path(os.environ.get("CORE_REPO") or cfg.CORE_REPO)
