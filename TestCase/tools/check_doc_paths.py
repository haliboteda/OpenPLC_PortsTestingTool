"""Every `$PORTTOOL/...` path written in OpenPLC_Docs names a file in this repo.

The product documents cite this repo's files by that prefix; nothing else checks
them, so a rename here would leave the documents pointing at nothing.

Exit 0 = all exist (or DOCS_REPO is not configured: reported as a skip),
1 = some do not.
"""

import re
import sys
from pathlib import Path

HERE = Path(__file__).resolve().parent
sys.path.insert(0, str(HERE))

from common import Fail, Ok, Section, Warn, cfg  # noqa: E402

REPO = HERE.parent.parent
# $PORTTOOL/a/b.go, $PORTTOOL:a/b.go, with an optional :line suffix.
PATH = re.compile(r"\$PORTTOOL(?:_REPO)?[:/]([\w./+-]+)")


def main():
    docs = Path(getattr(cfg, "DOCS_REPO", "") or "")
    if not str(docs) or not docs.is_dir():
        Warn("SKIP - DOCS_REPO not set; run tools/init_machine.py")
        return 0
    Section("every $PORTTOOL path in %s exists" % docs)
    missing, checked = [], 0
    for md in sorted(docs.rglob("*.md")):
        if ".git" in md.parts:
            continue
        for n, line in enumerate(md.read_text(encoding="utf-8", errors="replace").splitlines(), 1):
            for m in PATH.finditer(line):
                rel = re.sub(r":\d+(?:-\d+)?$", "", m.group(1).split("#")[0]).rstrip(".")
                if not rel or rel.endswith("/") and (REPO / rel).is_dir():
                    continue
                checked += 1
                if not (REPO / rel).exists():
                    missing.append("%s:%d  ->  $PORTTOOL/%s" % (md.relative_to(docs), n, rel))
    print("  %d reference(s) checked" % checked)
    for m in missing:
        Fail("  " + m)
    if missing:
        Fail("%d reference(s) name a file that does not exist" % len(missing))
        return 1
    Ok("every $PORTTOOL path named in OpenPLC_Docs exists")
    return 0


if __name__ == "__main__":
    sys.exit(main())
