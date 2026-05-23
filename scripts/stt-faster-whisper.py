#!/usr/bin/env python3
import argparse
import json
import os
import sys

from faster_whisper import WhisperModel


def main() -> int:
    parser = argparse.ArgumentParser(description="Transcribe audio with faster-whisper.")
    parser.add_argument("input", help="Audio file to transcribe")
    parser.add_argument("--model", default=os.getenv("CODEXCLAW_WHISPER_MODEL", "base"))
    parser.add_argument("--device", default=os.getenv("CODEXCLAW_WHISPER_DEVICE", "cpu"))
    parser.add_argument("--compute-type", default=os.getenv("CODEXCLAW_WHISPER_COMPUTE_TYPE", "int8"))
    parser.add_argument("--language", default=os.getenv("CODEXCLAW_WHISPER_LANGUAGE", ""))
    parser.add_argument("--beam-size", type=int, default=int(os.getenv("CODEXCLAW_WHISPER_BEAM_SIZE", "5")))
    parser.add_argument("--json", action="store_true", help="Print JSON with text and detected language")
    args = parser.parse_args()

    model = WhisperModel(args.model, device=args.device, compute_type=args.compute_type)
    segments, info = model.transcribe(
        args.input,
        beam_size=args.beam_size,
        language=args.language or None,
        vad_filter=True,
    )
    lines = [segment.text.strip() for segment in segments if segment.text.strip()]
    transcript = "\n".join(lines).strip()
    if not transcript:
        print("No speech detected.", file=sys.stderr)
        return 2
    if args.json:
        print(json.dumps({"text": transcript, "language": info.language or ""}, ensure_ascii=False))
        return 0
    print(transcript)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
