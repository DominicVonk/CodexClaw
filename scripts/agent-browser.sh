#!/usr/bin/env bash
set -euo pipefail

usage() {
  cat <<'USAGE'
Usage:
  scripts/agent-browser.sh install [agent-browser install args...]
  scripts/agent-browser.sh doctor [agent-browser doctor args...]
  scripts/agent-browser.sh upgrade [agent-browser upgrade args...]
  scripts/agent-browser.sh skill
  scripts/agent-browser.sh core [--full]

Commands:
  install  Install browser binaries through agent-browser.
  doctor   Diagnose the local agent-browser install.
  upgrade  Upgrade agent-browser using its detected install method.
  skill    Install the upstream vercel-labs/agent-browser skill globally for Codex.
  core     Print the version-matched agent-browser core skill from the CLI.

Environment:
  AGENT_BROWSER_COMMAND  Command to run. Default: agent-browser
  SKILLS_AGENT           Agent passed to skills.sh. Default: codex

Examples:
  mise run browser:install
  mise run browser:doctor -- --fix
  mise run browser:skill
  mise run browser:core -- --full
USAGE
}

if [[ $# -lt 1 || "${1:-}" == "-h" || "${1:-}" == "--help" ]]; then
  usage
  exit 0
fi

command="${AGENT_BROWSER_COMMAND:-agent-browser}"
action="$1"
shift

run_agent_browser() {
  if command -v "$command" >/dev/null 2>&1; then
    "$command" "$@"
  else
    npx --yes agent-browser "$@"
  fi
}

case "$action" in
  install)
    run_agent_browser install "$@"
    ;;
  doctor)
    run_agent_browser doctor "$@"
    ;;
  upgrade)
    run_agent_browser upgrade "$@"
    ;;
  skill)
    scripts/skills.sh add vercel-labs/agent-browser "$@"
    ;;
  core)
    run_agent_browser skills get core "$@"
    ;;
  *)
    echo "unknown command: $action" >&2
    usage >&2
    exit 2
    ;;
esac
