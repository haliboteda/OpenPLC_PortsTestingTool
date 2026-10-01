"""internal/calarea agrees with the bootloader's IAPServer/calib_area.h.

    python tools/check_calarea.py

The fixture writes the calibration area and the board reads it; if the two
layouts drift, every board reads its calibration as blank or corrupt and falls
back to nominal without a word. calib_area.h owns the format. Byte table:
$PROD/docs/modules/M1/SECTOR-15.md, "校准值区的格式".

Exit 0 = same layout, 1 = they differ or a side could not be parsed.
"""

import re
import sys
from pathlib import Path

HERE = Path(__file__).resolve().parent
sys.path.insert(0, str(HERE))

from common import cfg, Section, Ok, Fail, read_text  # noqa: E402

REPO = HERE.parent.parent


def layout_c(path):
    text = read_text(path)
    parts = []
    for k in ("CALIB_AREA_ADDR", "CALIB_MAGIC", "CALIB_VERSION", "CALIB_CHANNELS"):
        m = re.search(r'#define\s+%s\s+(0x[0-9A-Fa-f]+|\d+)' % k, text)
        if not m:
            return None
        parts.append("%s=%d" % (k.split("_")[-1], int(m.group(1).rstrip("UL"), 0)))
    m = re.search(r'sizeof\(calib_area_t\)\s*==\s*(\d+)', text)
    st = re.search(r'typedef struct \{([^{}]*)\} calib_area_t;', text)
    ch = re.search(r'typedef enum \{(.*?)\} calib_channel_id_t;', text, re.S)
    if not (m and st and ch):
        return None
    parts.append("SIZE=%s" % m.group(1))
    parts.append("fields=" + ",".join(re.findall(r'\b(\w+)(?:\[\w+\])?\s*;', st.group(1))))
    parts.append("channels=" + ",".join(re.findall(r'CALIB_CH_(\w+)', ch.group(1))))
    return ";".join(parts)


def layout_go(path):
    # Normalised to LF: the Go patterns below anchor on "\n".
    text = read_text(path).replace("\r\n", "\n")
    parts = []
    for c, k in (("Addr", "ADDR"), ("Magic", "MAGIC"), ("Version", "VERSION"), ("Channels", "CHANNELS")):
        m = re.search(r'\b%s\s*=\s*(0x[0-9A-Fa-f]+|\d+)' % c, text)
        if not m:
            return None
        parts.append("%s=%d" % (k, int(m.group(1), 0)))
    m = re.search(r'\bSize\s*=\s*(\d+)', text)
    st = re.search(r'type area struct \{(.*?)\n\}', text, re.S)
    ch = re.search(r'const \(\n\s*AI1 = iota(.*?)\n\)', text, re.S)
    if not (m and st and ch):
        return None
    parts.append("SIZE=%s" % m.group(1))
    parts.append("fields=" + ",".join(re.findall(r'^\s*(\w+)\s', st.group(1), re.M)))
    parts.append("channels=" + ",".join(["AI1"] + re.findall(r'^\s*(\w+)\s', ch.group(1), re.M)))
    return ";".join(parts)


def main():
    Section("calibration area layout")
    boot_h = Path(cfg.BOOT_REPO) / "IAPServer" / "calib_area.h"
    go = REPO / "internal" / "calarea" / "calarea.go"
    if not boot_h.exists():
        Fail("missing %s" % boot_h)
        return 1
    a, b = layout_c(boot_h), layout_go(go)
    print("  bootloader  %s" % a)
    print("  calarea     %s" % b)
    if a is None or b is None:
        Fail("could not parse one side - the extractor no longer matches the source")
        return 1
    # Go exports the fields capitalised; the names are the same field.
    if a.lower() != b.lower():
        Fail("the layouts differ - calib_area.h owns the format; change calarea.go to match")
        return 1
    Ok("same layout")
    return 0


if __name__ == "__main__":
    sys.exit(main())
