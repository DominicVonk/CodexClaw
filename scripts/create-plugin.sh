#!/usr/bin/env bash
set -euo pipefail

usage() {
  cat <<'USAGE'
Usage:
  scripts/create-plugin.sh <name> [description...]

Creates a Codex plugin skeleton at:
  ${PLUGIN_ROOT:-./plugins}/<normalized-name>/.codex-plugin/plugin.json

Example:
  mise run plugin:new -- repo-tools "Repository maintenance tools for Codex"
  mise run plugin:new -- "Repo Tools" "Repository maintenance tools for Codex"
USAGE
}

normalize_name() {
  printf '%s' "$1" \
    | tr '[:upper:]' '[:lower:]' \
    | sed -E 's/[^a-z0-9]+/-/g; s/^-+//; s/-+$//; s/-+/-/g' \
    | cut -c1-64 \
    | sed -E 's/-+$//'
}

json_escape() {
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
  description="Local Codex plugin for ${raw_name}."
fi
name="$(normalize_name "$raw_name")"
escaped_description="$(json_escape "$description")"

if [[ -z "$name" ]]; then
  echo "plugin name must contain at least one letter or number" >&2
  exit 2
fi

root="${PLUGIN_ROOT:-plugins}"
dir="$root/$name"
manifest="$dir/.codex-plugin/plugin.json"

if [[ -e "$dir" ]]; then
  echo "plugin already exists: $dir" >&2
  exit 1
fi

mkdir -p "$dir/.codex-plugin" "$dir/skills"
cat >"$manifest" <<EOF
{
  "name": "$name",
  "version": "0.0.0-alpha.1",
  "description": "$escaped_description"
}
EOF

touch "$dir/skills/.gitkeep"

echo "created $manifest"
