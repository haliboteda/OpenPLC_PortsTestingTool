#!/usr/bin/env bash
# The only build entry point: the menu and every rule live in tools/ptbuild.
# No arguments shows the menu; arguments run one item (tools/build.sh help).
cd "$(dirname "$0")/.." || exit 1

if ! command -v go >/dev/null 2>&1; then
  echo "Go is not installed or not on PATH. Install Go 1.23 or newer from https://go.dev/dl/ and open a new terminal."
  [ $# -eq 0 ] && read -r -p "Press Enter to close " _
  exit 2
fi

go run ./tools/ptbuild "$@"
rc=$?
# A double-clicked window closes before anyone has read the result.
[ $# -eq 0 ] && read -r -p "Press Enter to close " _
exit $rc
