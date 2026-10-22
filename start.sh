#!/usr/bin/env bash
# Double-click entry: opens the build menu. The menu itself lives in tools/build.sh (decision 149).
exec bash "$(dirname "$0")/tools/build.sh" "$@"
