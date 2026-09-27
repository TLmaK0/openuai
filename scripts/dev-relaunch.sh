#!/usr/bin/env bash
# Runs OpenUAI in a loop: when the app exits with code 42 (restart requested,
# e.g. POST /api/dev/restart) it is rebuilt and launched again.
#
# Usage: scripts/dev-relaunch.sh [--worktree DIR] [command...]
#   --worktree DIR  directory to build and run first (default: current dir)
#   command         what to run inside that directory (default: ./dev.sh)
#
# The app chooses the directory of the next launch: POST /api/dev/restart with
# {"worktree": "/abs/path"} writes that path to $OPENUAI_RELAUNCH_FILE before
# exiting with 42. Without a worktree it relaunches in the same directory.
set -u

dir="$PWD"
if [ "${1:-}" = "--worktree" ]; then
  if [ "$#" -lt 2 ]; then
    echo "--worktree needs a directory" >&2
    exit 2
  fi
  dir="$2"
  shift 2
fi

APP_CMD=("./dev.sh")
if [ "$#" -gt 0 ]; then
  APP_CMD=("$@")
fi

# Channel the app uses to tell us where to relaunch. Its presence in the
# environment is also how the app knows a relaunch loop is running.
OPENUAI_RELAUNCH_FILE="$(mktemp "${TMPDIR:-/tmp}/openuai-relaunch.XXXXXX")"
export OPENUAI_RELAUNCH_FILE
trap 'rm -f "$OPENUAI_RELAUNCH_FILE"' EXIT

while true; do
  if ! cd "$dir"; then
    echo "OpenUAI dev: cannot enter $dir" >&2
    exit 1
  fi
  echo "→ OpenUAI dev: running in $dir"
  : > "$OPENUAI_RELAUNCH_FILE"

  "${APP_CMD[@]}"
  code=$?

  if [ "$code" -ne 42 ]; then
    exit "$code"
  fi

  next="$(cat "$OPENUAI_RELAUNCH_FILE" 2>/dev/null)"
  if [ -n "$next" ]; then
    dir="$next"
  fi
  echo "OpenUAI requested restart; relaunching in $dir ..."
  sleep 1
done
