#!/usr/bin/env bash
set -euo pipefail

if [ "$#" -lt 2 ]; then
  echo "usage: $0 <text> <output.mp3>" >&2
  exit 2
fi

text="$1"
output="$2"
language="${CODEXCLAW_TTS_LANGUAGE:-}"
voice="${CODEXCLAW_EDGE_TTS_VOICE:-}"
if [ -z "$voice" ]; then
  case "$language" in
    nl*) voice="${CODEXCLAW_EDGE_TTS_VOICE_NL:-nl-NL-ColetteNeural}" ;;
    de*) voice="${CODEXCLAW_EDGE_TTS_VOICE_DE:-de-DE-KatjaNeural}" ;;
    fr*) voice="${CODEXCLAW_EDGE_TTS_VOICE_FR:-fr-FR-DeniseNeural}" ;;
    es*) voice="${CODEXCLAW_EDGE_TTS_VOICE_ES:-es-ES-ElviraNeural}" ;;
    *) voice="en-US-AriaNeural" ;;
  esac
fi
rate="${CODEXCLAW_EDGE_TTS_RATE:-+0%}"
volume="${CODEXCLAW_EDGE_TTS_VOLUME:-+0%}"

uv run --with edge-tts edge-tts \
  --voice "$voice" \
  --rate "$rate" \
  --volume "$volume" \
  --text "$text" \
  --write-media "$output"
