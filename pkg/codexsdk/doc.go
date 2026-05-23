// Package codexsdk is a Go port of the TypeScript @openai/codex-sdk.
//
// It wraps the Codex CLI by spawning:
//
//	codex exec --experimental-json
//
// Prompts are written to stdin, JSONL events are read from stdout, and repeated
// runs on the same Thread continue the conversation by passing resume <threadID>.
package codexsdk
