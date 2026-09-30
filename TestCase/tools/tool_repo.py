"""Puts IAPTranfer_Tool's TestCase/tools on sys.path, so `from common import ...` works here.

Machine-local paths have one home: IAPTranfer_Tool/TestCase/config/machine.py,
read through its common.py. This repo borrows that instead of keeping a second
copy. See $PROD/docs/tables/DECISIONS.md, decision 76.

Where IAPTranfer_Tool is, first match wins: $OPENPLC_TOOL_REPO, then the
sibling directory - the layout init_machine.py assumes for every repo.
"""

import os
import sys
from pathlib import Path

REPO = Path(__file__).resolve().parents[2]


def _find():
    named = os.environ.get("OPENPLC_TOOL_REPO", "")
    for cand in ([Path(named)] if named else []) + [REPO.parent / "IAPTranfer_Tool"]:
        if (cand / "TestCase" / "tools" / "common.py").is_file():
            return cand
    sys.exit("IAPTranfer_Tool not found beside %s. Clone it there, or set "
             "OPENPLC_TOOL_REPO to its checkout." % REPO)


TOOL_REPO = _find()
sys.path.insert(0, str(TOOL_REPO / "TestCase" / "tools"))
