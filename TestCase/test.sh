#!/usr/bin/env bash
# Every check of this repo that needs no board. No arguments shows a menu.
#
#   TestCase/test.sh all     every step
#   TestCase/test.sh quick   every step but T4-02 (the browser run, a few minutes)
#   TestCase/test.sh go-test | go-vet | t4-01 | t4-05 | t4-02
#
# Case definitions: $PROD/docs/engineering/HOW-TO-RUN-PORTTOOL-TESTS.md.
# Exit 0 = everything that ran passed, 1 = a step failed, 2 = bad usage.
cd "$(dirname "$0")/.." || exit 1
results=()
failed=0

step() { # id, what it proves, command...
  local id="$1" what="$2"
  shift 2
  echo
  echo "===== $id  $what"
  if "$@"; then
    results+=("$id  PASS  $what")
  else
    results+=("$id  FAIL  $what")
    failed=$((failed + 1))
  fi
}

go_test() { step GO-TEST "Go unit and integration tests, panel dictionary included (T4-04 too)" go test -count=1 ./...; }
go_vet()  { step GO-VET  "go vet over the whole module" go vet ./...; }
t4_01_run() {
  local cc
  cc=$(go run ./tools/ptbuild get HOST_CC) || return 1
  CGO_ENABLED=1 CC="$cc" go test -race -count=1 -v ./TestCase/host/porttool_caps/ -args -build
}
t4_01()   { step T4-01   "port tool protocol contract (real porttool.c, then the Go parser)" t4_01_run; }
t4_05()   { step T4-05   "the step-6 demo speaks the fixture protocol (one Go test, both boards)" go test -count=1 -v ./TestCase/host/porttool_demo/ -args -build; }
# T4-02 drives the built PortTool and the simulated board, so both are rebuilt
# first: a stale one would test yesterday's code.
t4_02_run() {
  local os=windows
  case "$(uname -s)" in Linux) os=linux ;; Darwin) os=darwin; [ "$(uname -m)" = arm64 ] && os=darwin-arm64 ;; esac
  tools/build.sh tool --os "$os" >/dev/null && tools/build.sh sim >/dev/null || { echo "could not build PortTool or the simulated board"; return 1; }
  go run ./TestCase/host/porttool_panel --port sim
}
t4_02()   { step T4-02   "the panel clicked through in a real browser, against the simulated board" t4_02_run; }

pick="$1"
if [ -z "$pick" ]; then
  echo
  echo "  What to run?"
  echo "    1) all"
  echo "    2) quick  (all but T4-02)"
  echo "    3) go test ./..."
  echo "    4) go vet ./..."
  echo "    5) T4-01"
  echo "    6) T4-05"
  echo "    7) T4-02  (browser, simulated board)"
  echo
  read -r -p "  Choose [1-7, Enter=2]: " n || exit 2
  case "${n:-2}" in
    1) pick=all ;; 2) pick=quick ;; 3) pick=go-test ;; 4) pick=go-vet ;;
    5) pick=t4-01 ;; 6) pick=t4-05 ;; 7) pick=t4-02 ;; *) echo "no such item: $n"; exit 2 ;;
  esac
fi

case "$pick" in
  all)     go_test; go_vet; t4_01; t4_05; t4_02 ;;
  quick)   go_test; go_vet; t4_01; t4_05 ;;
  go-test) go_test ;;
  go-vet)  go_vet ;;
  t4-01)   t4_01 ;;
  t4-05)   t4_05 ;;
  t4-02)   t4_02 ;;
  *) echo "usage: TestCase/test.sh [all|quick|go-test|go-vet|t4-01|t4-05|t4-02]"; exit 2 ;;
esac

echo
echo "===== summary"
printf '%s\n' "${results[@]}"
[ "$failed" -eq 0 ] || { echo "$failed step(s) failed"; exit 1; }
echo "host-side checks pass"
