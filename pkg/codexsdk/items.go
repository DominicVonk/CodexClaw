package codexsdk

import "encoding/json"

// ThreadItem is a decoded item emitted by codex exec.
type ThreadItem struct {
	ID               string             `json:"id,omitempty"`
	Type             string             `json:"type,omitempty"`
	Command          string             `json:"command,omitempty"`
	AggregatedOutput string             `json:"aggregated_output,omitempty"`
	ExitCode         *int               `json:"exit_code,omitempty"`
	Status           string             `json:"status,omitempty"`
	Changes          []FileUpdateChange `json:"changes,omitempty"`
	Server           string             `json:"server,omitempty"`
	Tool             string             `json:"tool,omitempty"`
	Arguments        any                `json:"arguments,omitempty"`
	Result           any                `json:"result,omitempty"`
	Error            *ItemError         `json:"error,omitempty"`
	Text             string             `json:"text,omitempty"`
	Query            string             `json:"query,omitempty"`
	Items            []TodoItem         `json:"items,omitempty"`
	Raw              json.RawMessage    `json:"-"`
}

// FileUpdateChange describes one path changed by a file_change item.
type FileUpdateChange struct {
	Path string `json:"path,omitempty"`
	Kind string `json:"kind,omitempty"`
}

// ItemError describes an item-level error.
type ItemError struct {
	Message string `json:"message,omitempty"`
}

// TodoItem describes one todo_list entry.
type TodoItem struct {
	Text      string `json:"text,omitempty"`
	Completed bool   `json:"completed,omitempty"`
}

func decodeThreadItem(raw json.RawMessage) (ThreadItem, error) {
	var item ThreadItem
	if len(raw) == 0 {
		return item, nil
	}
	if err := json.Unmarshal(raw, &item); err != nil {
		return ThreadItem{}, err
	}
	item.Raw = append(item.Raw[:0], raw...)
	return item, nil
}
