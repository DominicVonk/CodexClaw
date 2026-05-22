#!/usr/bin/env bash
set -euo pipefail

usage() {
  cat <<'USAGE'
Usage:
  scripts/create-skill.sh <name> [description...]

Creates a Codex skill skeleton at:
  ${SKILL_ROOT:-./skills}/<normalized-name>/SKILL.md

Example:
  mise run skill:new -- repo-maintainer "Review and maintain this repository"
  mise run skill:new -- "Repo Maintainer" "Review and maintain this repository"
USAGE
}

normalize_name() {
  printf '%s' "$1" \
    | tr '[:upper:]' '[:lower:]' \
    | sed -E 's/[^a-z0-9]+/-/g; s/^-+//; s/-+$//; s/-+/-/g'
}

yaml_escape() {
  printf '%s' "$1" \
    | sed -e 's/\\/\\\\/g' -e 's/"/\\"/g'
}

if [[ $# -lt 1 || "${1:-}" == "-h" || "${1:-}" == "--help" ]]; then
  usage
  exit 0
fi

raw_name="$1"
shift
if [[ $# -gt 0 ]]; then
  description="$*"
else
  description="Use when Codex needs focused instructions for ${raw_name}."
fi
name="$(normalize_name "$raw_name")"
escaped_description="$(yaml_escape "$description")"

if [[ -z "$name" ]]; then
  echo "skill name must contain at least one letter or number" >&2
  exit 2
fi

root="${SKILL_ROOT:-skills}"
dir="$root/$name"
file="$dir/SKILL.md"

if [[ -e "$dir" ]]; then
  echo "skill already exists: $dir" >&2
  exit 1
fi

mkdir -p "$dir"
cat >"$file" <<EOF
---
name: $name
description: "$escaped_description"
---

# $raw_name

Use this skill when the task matches the description above.

## Workflow

1. Read the request and identify the relevant inputs.
2. Apply the local project conventions before introducing new patterns.
3. Keep context compact and load only the files needed for the task.
EOF

echo "created $file"
