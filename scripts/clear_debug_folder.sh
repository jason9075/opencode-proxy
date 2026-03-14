#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
DEBUG_DIR="$(cd "$SCRIPT_DIR/.." && pwd)/debug"

if [[ ! -d "$DEBUG_DIR" ]]; then
  echo "debug folder not found: $DEBUG_DIR"
  exit 0
fi

file_count=$(find "$DEBUG_DIR" -type f | wc -l)

if [[ "$file_count" -eq 0 ]]; then
  echo "debug folder is already empty"
  exit 0
fi

echo "clearing $file_count file(s) in $DEBUG_DIR"
rm -rf "${DEBUG_DIR:?}"/*
echo "done"
