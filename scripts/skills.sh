#!/usr/bin/env bash
set -euo pipefail

usage() {
  cat <<'USAGE'
Usage:
  scripts/skills.sh add <owner/repo|package> [skills args...]
  scripts/skills.sh list [skills args...]
  scripts/skills.sh find [query] [skills args...]
  scripts/skills.sh update [skills args...]
  scripts/skills.sh init <name> [skills args...]
  scripts/skills.sh sync [skills args...]
  scripts/skills.sh restore [skills args...]

Environment:
  SKILLS_AGENT     Agent passed to skills.sh for installs. Default: codex
  DISABLE_TELEMETRY Set to 0 to allow skills.sh telemetry. Default: 1

Examples:
  mise run skills:add -- vercel-labs/agent-skills
  mise run skills:find -- github
  mise run skills:update
USAGE
}

if [[ $# -lt 1 || "${1:-}" == "-h" || "${1:-}" == "--help" ]]; then
  usage
  exit 0
fi

export DISABLE_TELEMETRY="${DISABLE_TELEMETRY:-1}"

command="$1"
shift

run_skills() {
  npx --yes skills "$@"
}

case "$command" in
  add)
    if [[ $# -lt 1 ]]; then
      echo "missing package, e.g. vercel-labs/agent-skills" >&2
      exit 2
    fi
    package="$1"
    shift
    run_skills add "$package" --global --agent "${SKILLS_AGENT:-codex}" --yes --copy "$@"
    ;;
  list)
    if [[ $# -eq 0 ]]; then
      run_skills list --global
    else
      run_skills list "$@"
    fi
    ;;
  find)
    run_skills find "$@"
    ;;
  update)
    run_skills update "$@"
    ;;
  init)
    run_skills init "$@"
    ;;
  sync)
    run_skills experimental_sync "$@"
    ;;
  restore)
    run_skills experimental_install "$@"
    ;;
  *)
    echo "unknown command: $command" >&2
    usage >&2
    exit 2
    ;;
esac
