package codexsdk

import (
	"encoding/json"
	"errors"
	"fmt"
)

// Usage describes token usage emitted by codex exec.
type Usage struct {
	InputTokens           int64 `json:"input_tokens"`
	CachedInputTokens     int64 `json:"cached_input_tokens"`
	OutputTokens          int64 `json:"output_tokens"`
	ReasoningOutputTokens int64 `json:"reasoning_output_tokens"`
	TotalTokens           int64 `json:"total_tokens,omitempty"`
}

// Total returns total_tokens when present, otherwise input_tokens + output_tokens.
func (u Usage) Total() int64 {
	if u.TotalTokens > 0 {
		return u.TotalTokens
	}
	return u.InputTokens + u.OutputTokens
}

// ThreadError describes a turn failure.
type ThreadError struct {
	Message string `json:"message"`
}

// ThreadEvent is a top-level JSONL event emitted by codex exec.
type ThreadEvent struct {
	Type     string          `json:"type"`
	ThreadID string          `json:"thread_id,omitempty"`
	Item     ThreadItem      `json:"item,omitempty"`
	Usage    Usage           `json:"usage,omitempty"`
	Error    *ThreadError    `json:"error,omitempty"`
	Message  string          `json:"message,omitempty"`
	Raw      json.RawMessage `json:"-"`
}

type rawThreadEvent struct {
	Type     string          `json:"type"`
	ThreadID string          `json:"thread_id"`
	Item     json.RawMessage `json:"item"`
	Usage    Usage           `json:"usage"`
	Error    *ThreadError    `json:"error"`
	Message  string          `json:"message"`
}

// ParseEvent decodes one JSONL event line.
func ParseEvent(line []byte) (ThreadEvent, error) {
	if len(line) == 0 {
		return ThreadEvent{}, errors.New("empty codex event")
	}
	var raw rawThreadEvent
	if err := json.Unmarshal(line, &raw); err != nil {
		return ThreadEvent{}, fmt.Errorf("parse codex event: %w", err)
	}
	item, err := decodeThreadItem(raw.Item)
	if err != nil {
		return ThreadEvent{}, fmt.Errorf("parse codex item: %w", err)
	}
	return ThreadEvent{
		Type:     raw.Type,
		ThreadID: raw.ThreadID,
		Item:     item,
		Usage:    raw.Usage,
		Error:    raw.Error,
		Message:  raw.Message,
		Raw:      append(json.RawMessage(nil), line...),
	}, nil
}
