"""Every check of this repo that needs no board.

    python tools/selfcheck.py           all steps
    python tools/selfcheck.py --quick   skip T4-02 (the browser run, a few minutes)
    python tools/selfcheck.py --list    what each step proves

Case definitions: $PROD/docs/engineering/HOW-TO-RUN-TESTS.md.

Exit 0 = everything that ran passed (a SKIP names what is missing), 1 = a step failed.
"""

import argparse
import shutil
import subprocess
import sys
from pathlib import Path

try:
    sys.stdout.reconfigure(line_buffering=True)
except AttributeError:
    pass

HERE = Path(__file__).resolve().parent
sys.path.insert(0, str(HERE))

from common import EXE, PLATFORM, cfg, Section, Ok, Warn, Fail  # noqa: E402

REPO = HERE.parent.parent
TESTCASE = HERE.parent
GOOS_DIR = {"windows": "windows", "linux": "linux", "macos": "darwin"}[PLATFORM]

# (id, what it proves)
CATALOG = [
    ("ENV", "this machine's paths resolve (config/machine.py)"),
    ("GO-TEST", "Go unit tests: parser, judging, plan executor, panel API (T4-04 included)"),
    ("GO-VET", "go vet over the whole module"),
    ("T4-01", "port tool protocol contract (real porttool.c, then the Go parser)"),
    ("CALAREA", "internal/calarea has the bootloader's calibration area layout"),
    ("T4-02", "the panel clicked through in a real browser, against the simulated board"),
    ("DOCS", "every $PORTTOOL/... path written in OpenPLC_Docs exists"),
]

results = []


def run(step, argv, cwd, needs=None):
    Section("%s  %s" % (step, dict(CATALOG)[step]))
    if needs and not shutil.which(needs) and not Path(needs).exists():
        Warn("SKIP - %s not found" % needs)
        results.append((step, "SKIP", "%s missing" % needs))
        return False
    rc = subprocess.call([str(a) for a in argv], cwd=str(cwd))
    state = "PASS" if rc == 0 else "FAIL (exit %d)" % rc
    (Ok if rc == 0 else Fail)(state)
    results.append((step, state, ""))
    return rc == 0


def env():
    Section("ENV  %s" % dict(CATALOG)["ENV"])
    missing = [k for k in ("BOOT_REPO", "CUBEIDE", "HOST_CC", "GIT_BASH")
               if not getattr(cfg, k, "") or not Path(getattr(cfg, k)).exists()]
    for k in ("BOOT_REPO", "CUBEIDE", "WORKSPACE", "HOST_CC", "GIT_BASH"):
        print("  %-10s %s" % (k, getattr(cfg, k, "")))
    if missing:
        Warn("SKIP - missing: %s (run tools/init_machine.py)" % ", ".join(missing))
        results.append(("ENV", "SKIP", ", ".join(missing)))
    else:
        Ok("PASS")
        results.append(("ENV", "PASS", ""))


def main():
    ap = argparse.ArgumentParser(description=__doc__,
                                 formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("--quick", action="store_true")
    ap.add_argument("--list", action="store_true")
    args = ap.parse_args()
    if args.list:
        for step, what in CATALOG:
            print("  %-8s %s" % (step, what))
        return 0

    env()
    run("GO-TEST", ["go", "test", "./..."], REPO, needs="go")
    run("GO-VET", ["go", "vet", "./..."], REPO, needs="go")
    run("T4-01", [sys.executable, TESTCASE / "host" / "porttool_caps" / "build.py"], REPO,
        needs=getattr(cfg, "HOST_CC", "") or "gcc")
    run("CALAREA", [sys.executable, HERE / "check_calarea.py"], TESTCASE)
    run("DOCS", [sys.executable, HERE / "check_doc_paths.py"], TESTCASE)
    if args.quick:
        results.append(("T4-02", "SKIP", "--quick"))
    else:
        # T4-02 drives the built executable and the simulated board, so both are
        # rebuilt first: a stale one would test yesterday's code.
        exe = REPO / "Output" / GOOS_DIR / ("PortTool" + EXE)
        built = subprocess.call(["go", "build", "-o", str(exe), "./cmd/porttool"], cwd=str(REPO)) == 0
        sim = subprocess.call([sys.executable, "build.py", "--sim"],
                              cwd=str(TESTCASE / "host" / "porttool_caps"),
                              stdout=subprocess.DEVNULL) == 0
        if built and sim:
            run("T4-02", [sys.executable, "run.py", "--port", "sim"],
                TESTCASE / "host" / "porttool_panel")
        else:
            Fail("could not build PortTool or the simulated board for T4-02")
            results.append(("T4-02", "FAIL (build)", ""))

    Section("summary")
    for step, state, note in results:
        print("%-8s %-58s %s%s" % (step, dict(CATALOG)[step], state,
                                   ("  (" + note + ")") if note else ""))
    failed = [r for r in results if r[1].startswith("FAIL")]
    if failed:
        Fail("%d failed" % len(failed))
        return 1
    Ok("host-side checks pass")
    return 0


if __name__ == "__main__":
    sys.exit(main())
