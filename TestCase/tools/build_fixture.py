"""Builds the fixture (port tool) firmware image headlessly, from $BOOT.

    python tools/build_fixture.py

The fixture image is the bootloader project built with one extra preprocessor
symbol and its own linker script. Both are passed on the command line only:

    -D PORTTOOL_ENABLE=1   -E PLC_LD_SCRIPT=STM32H743IIKX_FLASH_PORTTOOL.ld

so neither is ever written into .cproject, and a build from inside the IDE
stays a bootloader build. The log is checked for both, because a symbol or a
script that silently did not arrive yields an image that looks fine and is not
the fixture.

⚠️ CubeIDE must be CLOSED: a headless build cannot take a locked workspace.
⚠️ Afterwards $BOOT/Debug/ holds the fixture image, which is not a bootloader.

Exit 0 = built, 1 = build failed or the log does not prove it is the fixture.
"""

import re
import subprocess
import sys
from pathlib import Path

HERE = Path(__file__).resolve().parent
sys.path.insert(0, str(HERE))

from common import cfg, get_cube_ide_exe, Section, Ok, Warn, Fail  # noqa: E402

PROJECT = "open_plc_cube_ide/Debug"

# The marker the fixture image must carry (DECISIONS.md 14): a #warning, so it
# cannot be missed in a build log.
TOOL_MARKER = "PORTTOOL_ENABLE=1: this image is the hardware test tool"

# Warnings that follow from a deliberate design choice in $BOOT, matched on an
# exact substring so nothing else hides behind them. See $PROD/docs/modules/M1/FLASHBOOT.md
KNOWN_WARNINGS = ("LOAD segment with RWX permissions",)

TOOL_LD = "STM32H743IIKX_FLASH_PORTTOOL.ld"
BOOT_LD = "STM32H743IIKX_FLASH.ld"


def build():
    Section("Build: fixture image")
    argv = [str(get_cube_ide_exe()), "--launcher.suppressErrors", "-nosplash",
            "-application", "org.eclipse.cdt.managedbuilder.core.headlessbuild",
            "-data", str(cfg.WORKSPACE),
            "-D", "PORTTOOL_ENABLE=1", "-E", "PLC_LD_SCRIPT=%s" % TOOL_LD,
            # Clean: objects from a bootloader build are still on disk and make
            # does not know the macro changed.
            "-cleanBuild", PROJECT]
    out = subprocess.run(argv, stdout=subprocess.PIPE, stderr=subprocess.STDOUT,
                         text=True, errors="replace").stdout or ""

    # A compile error stops make before "Build Finished" is printed.
    if re.search(r"(?m)^make:.*Error \d+", out) or re.search(r"(?m):\d+:\d+: error:", out):
        Fail("the build failed")
        for line in out.splitlines():
            if re.search(r"error:|Error \d+|overflowed", line):
                print("    %s" % line)
        return False

    finished = re.findall(r"Build Finished\. (\d+) errors?, (\d+) warnings?", out)
    if not finished:
        Fail("the build never reported finishing")
        print(out[-2000:])
        return False
    errors, warnings = finished[-1]
    if errors != "0":
        Fail("%s errors" % errors)
        return False

    if ("-T" + BOOT_LD) in out.replace('"', '') or (" " + BOOT_LD) in out:
        Fail("the linker was given %s, not %s - check the PLC_LD_SCRIPT default in "
             ".settings/org.eclipse.cdt.core.prefs" % (BOOT_LD, TOOL_LD))
        return False
    if TOOL_LD not in out:
        Fail("no linker line names %s - did PLC_LD_SCRIPT expand? See "
             "$PROD/docs/build/CUBEMX-RULES.md" % TOOL_LD)
        return False
    Ok("linked against %s" % TOOL_LD)

    if TOOL_MARKER not in out:
        # The symbol did not reach the compiler: what was built is a bootloader.
        Fail("the PORTTOOL_ENABLE marker is not in the build log")
        return False

    expected = 1 + sum(1 for line in out.splitlines()
                       if any(k in line for k in KNOWN_WARNINGS))
    if int(warnings) > expected:
        Warn("%s warnings (expected %d)" % (warnings, expected))
        for line in out.splitlines():
            if ("warning:" in line and TOOL_MARKER not in line
                    and not any(k in line for k in KNOWN_WARNINGS)):
                print("    %s" % line)
    else:
        Ok("0 errors, %s warning(s) - as expected" % warnings)

    binary = Path(cfg.BOOT_REPO) / "Debug" / "open_plc_cube_ide.bin"
    if not binary.exists():
        Fail("no .bin at %s" % binary)
        return False
    # No size limit: ST-Link writes it whole, IAP never carries it.
    Ok("fixture image: %d bytes" % binary.stat().st_size)
    Warn("$BOOT/Debug/ now holds the FIXTURE image, which is not a bootloader.")
    return True


if __name__ == "__main__":
    sys.exit(0 if build() else 1)
