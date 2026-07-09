#!/usr/bin/env bash
set -u

APP_CMD=("./dev.sh")
if [ "$#" -gt 0 ]; then
  APP_CMD=("$@")
fi

while true; do
  "${APP_CMD[@]}"
  code=$?

  if [ "$code" -ne 42 ]; then
    exit "$code"
  fi

  echo "OpenUAI requested restart; relaunching in dev mode..."
  sleep 1
done
