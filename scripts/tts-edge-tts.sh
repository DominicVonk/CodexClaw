#!/usr/bin/env bash
set -euo pipefail

if [ "$#" -lt 2 ]; then
  echo "usage: $0 <text> <output.mp3>" >&2
  exit 2
fi

text="$1"
output="$2"
voice="${CODEXCLAW_EDGE_TTS_VOICE:-en-US-AriaNeural}"
rate="${CODEXCLAW_EDGE_TTS_RATE:-+0%}"
volume="${CODEXCLAW_EDGE_TTS_VOLUME:-+0%}"

uv run --with edge-tts edge-tts \
  --voice "$voice" \
  --rate "$rate" \
  --volume "$volume" \
  --text "$text" \
  --write-media "$output"
