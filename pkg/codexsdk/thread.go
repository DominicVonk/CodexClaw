package codexsdk

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// Turn is the completed result returned by Thread.Run.
type Turn struct {
	Items         []ThreadItem
	FinalResponse string
	Usage         *Usage
}

// Thread represents a Codex conversation.
type Thread struct {
	exec          *Exec
	options       Options
	threadOptions ThreadOptions
	id            string
}

func newThread(exec *Exec, options Options, threadOptions ThreadOptions, id string) *Thread {
	return &Thread{
		exec:          exec,
		options:       options,
		threadOptions: threadOptions,
		id:            id,
	}
}

// ID returns the thread ID. It is empty until a new thread has emitted thread.started.
func (t *Thread) ID() string {
	return t.id
}

// RunStreamed streams events as Codex emits them.
func (t *Thread) RunStreamed(ctx context.Context, input []UserInput, turnOptions TurnOptions) (<-chan ThreadEvent, <-chan error) {
	events := make(chan ThreadEvent)
	errs := make(chan error, 1)
	go func() {
		defer close(events)
		errs <- t.runStreamed(ctx, input, turnOptions, events)
		close(errs)
	}()
	return events, errs
}

func (t *Thread) runStreamed(ctx context.Context, input []UserInput, turnOptions TurnOptions, events chan<- ThreadEvent) error {
	prompt, images := normalizeInput(input)
	schemaFile, err := createOutputSchemaFile(turnOptions.OutputSchema)
	if err != nil {
		return err
	}
	defer schemaFile.Cleanup()

	lines, errs := t.exec.Run(ctx, ExecArgs{
		Input:                 prompt,
		BaseURL:               t.options.BaseURL,
		APIKey:                t.options.APIKey,
		ThreadID:              t.id,
		Images:                images,
		Model:                 t.threadOptions.Model,
		SandboxMode:           t.threadOptions.SandboxMode,
		WorkingDirectory:      t.threadOptions.WorkingDirectory,
		AdditionalDirectories: t.threadOptions.AdditionalDirectories,
		SkipGitRepoCheck:      t.threadOptions.SkipGitRepoCheck,
		OutputSchemaFile:      schemaFile.Path,
		ModelReasoningEffort:  t.threadOptions.ModelReasoningEffort,
		NetworkAccessEnabled:  t.threadOptions.NetworkAccessEnabled,
		WebSearchMode:         t.threadOptions.WebSearchMode,
		WebSearchEnabled:      t.threadOptions.WebSearchEnabled,
		ApprovalPolicy:        t.threadOptions.ApprovalPolicy,
	})
	for line := range lines {
		event, err := ParseEvent([]byte(line))
		if err != nil {
			return err
		}
		if event.Type == "thread.started" && event.ThreadID != "" {
			t.id = event.ThreadID
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case events <- event:
		}
	}
	if err := <-errs; err != nil {
		return err
	}
	return nil
}

// Run buffers events until the turn finishes.
func (t *Thread) Run(ctx context.Context, input []UserInput, turnOptions TurnOptions) (Turn, error) {
	events, errs := t.RunStreamed(ctx, input, turnOptions)
	turn := Turn{}
	for event := range events {
		switch event.Type {
		case "item.completed":
			if event.Item.Type == "agent_message" {
				turn.FinalResponse = event.Item.Text
			}
			turn.Items = append(turn.Items, event.Item)
		case "turn.completed":
			usage := event.Usage
			turn.Usage = &usage
		case "turn.failed":
			if event.Error != nil && event.Error.Message != "" {
				return Turn{}, errors.New(event.Error.Message)
			}
			return Turn{}, errors.New("codex turn failed")
		case "error":
			if strings.TrimSpace(event.Message) != "" {
				return Turn{}, errors.New(event.Message)
			}
		}
	}
	if err := <-errs; err != nil {
		return Turn{}, err
	}
	return turn, nil
}

// RunText is a convenience wrapper around Run for a single text prompt.
func (t *Thread) RunText(ctx context.Context, input string, turnOptions TurnOptions) (Turn, error) {
	return t.Run(ctx, []UserInput{TextInput(input)}, turnOptions)
}

// RunStreamedText is a convenience wrapper around RunStreamed for a single text prompt.
func (t *Thread) RunStreamedText(ctx context.Context, input string, turnOptions TurnOptions) (<-chan ThreadEvent, <-chan error) {
	return t.RunStreamed(ctx, []UserInput{TextInput(input)}, turnOptions)
}

func normalizeInput(input []UserInput) (string, []string) {
	promptParts := make([]string, 0, len(input))
	images := make([]string, 0)
	for _, item := range input {
		switch strings.ToLower(item.Type) {
		case "text":
			promptParts = append(promptParts, item.Text)
		case "local_image", "localimage":
			if item.Path != "" {
				images = append(images, item.Path)
			}
		}
	}
	return strings.Join(promptParts, "\n\n"), images
}

func (u UserInput) String() string {
	switch strings.ToLower(u.Type) {
	case "text":
		return u.Text
	case "local_image", "localimage":
		return fmt.Sprintf("local_image:%s", u.Path)
	default:
		return u.Type
	}
}
