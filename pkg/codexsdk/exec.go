package codexsdk

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strings"
)

const (
	internalOriginatorEnv = "CODEX_INTERNAL_ORIGINATOR_OVERRIDE"
	goSDKOriginator       = "codex_sdk_go"
)

// ExecArgs contains one codex exec invocation.
type ExecArgs struct {
	Input                 string
	BaseURL               string
	APIKey                string
	ThreadID              string
	Images                []string
	Model                 string
	SandboxMode           SandboxMode
	WorkingDirectory      string
	AdditionalDirectories []string
	SkipGitRepoCheck      bool
	OutputSchemaFile      string
	ModelReasoningEffort  ModelReasoningEffort
	NetworkAccessEnabled  *bool
	WebSearchMode         WebSearchMode
	WebSearchEnabled      *bool
	ApprovalPolicy        ApprovalMode
}

// Exec runs the Codex CLI and streams raw JSONL lines.
type Exec struct {
	executablePath  string
	envOverride     map[string]string
	configOverrides ConfigObject
}

func newExec(options Options) *Exec {
	path := options.CodexPathOverride
	if strings.TrimSpace(path) == "" {
		path = defaultCodexBinary()
	}
	return &Exec{
		executablePath:  path,
		envOverride:     cloneStringMap(options.Env),
		configOverrides: options.Config,
	}
}

func defaultCodexBinary() string {
	if runtime.GOOS == "windows" {
		return "codex.exe"
	}
	return "codex"
}

// CommandArgs returns the command-line arguments for a codex exec invocation.
func (e *Exec) CommandArgs(args ExecArgs) ([]string, error) {
	commandArgs := []string{"exec", "--experimental-json"}
	overrides, err := serializeConfigOverrides(e.configOverrides)
	if err != nil {
		return nil, err
	}
	for _, override := range overrides {
		commandArgs = append(commandArgs, "--config", override)
	}
	if args.BaseURL != "" {
		baseURL, err := toTomlValue(args.BaseURL, "openai_base_url")
		if err != nil {
			return nil, err
		}
		commandArgs = append(commandArgs, "--config", "openai_base_url="+baseURL)
	}
	if args.Model != "" {
		commandArgs = append(commandArgs, "--model", args.Model)
	}
	if args.SandboxMode != "" {
		commandArgs = append(commandArgs, "--sandbox", string(args.SandboxMode))
	}
	if args.WorkingDirectory != "" {
		commandArgs = append(commandArgs, "--cd", args.WorkingDirectory)
	}
	for _, dir := range args.AdditionalDirectories {
		commandArgs = append(commandArgs, "--add-dir", dir)
	}
	if args.SkipGitRepoCheck {
		commandArgs = append(commandArgs, "--skip-git-repo-check")
	}
	if args.OutputSchemaFile != "" {
		commandArgs = append(commandArgs, "--output-schema", args.OutputSchemaFile)
	}
	if args.ModelReasoningEffort != "" {
		commandArgs = append(commandArgs, "--config", fmt.Sprintf("model_reasoning_effort=%q", args.ModelReasoningEffort))
	}
	if args.NetworkAccessEnabled != nil {
		commandArgs = append(commandArgs, "--config", fmt.Sprintf("sandbox_workspace_write.network_access=%t", *args.NetworkAccessEnabled))
	}
	if args.WebSearchMode != "" {
		commandArgs = append(commandArgs, "--config", fmt.Sprintf("web_search=%q", args.WebSearchMode))
	} else if args.WebSearchEnabled != nil {
		if *args.WebSearchEnabled {
			commandArgs = append(commandArgs, "--config", `web_search="live"`)
		} else {
			commandArgs = append(commandArgs, "--config", `web_search="disabled"`)
		}
	}
	if args.ApprovalPolicy != "" {
		commandArgs = append(commandArgs, "--config", fmt.Sprintf("approval_policy=%q", args.ApprovalPolicy))
	}
	if strings.TrimSpace(args.ThreadID) != "" {
		commandArgs = append(commandArgs, "resume", args.ThreadID)
	}
	for _, image := range args.Images {
		commandArgs = append(commandArgs, "--image", image)
	}
	return commandArgs, nil
}

// Run starts codex exec and returns raw JSONL lines and a completion error channel.
func (e *Exec) Run(ctx context.Context, args ExecArgs) (<-chan string, <-chan error) {
	lines := make(chan string)
	errs := make(chan error, 1)
	go func() {
		defer close(lines)
		errs <- e.run(ctx, args, lines)
		close(errs)
	}()
	return lines, errs
}

func (e *Exec) run(ctx context.Context, args ExecArgs, lines chan<- string) error {
	commandArgs, err := e.CommandArgs(args)
	if err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, e.executablePath, commandArgs...)
	cmd.Env = e.env(args)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return err
	}
	writeErr := make(chan error, 1)
	go func() {
		_, err := io.WriteString(stdin, args.Input)
		closeErr := stdin.Close()
		if err != nil {
			writeErr <- err
			return
		}
		writeErr <- closeErr
	}()
	scanErr := scanLines(ctx, stdout, lines)
	waitErr := cmd.Wait()
	if err := <-writeErr; err != nil && scanErr == nil && waitErr == nil {
		return err
	}
	if scanErr != nil {
		return scanErr
	}
	if waitErr != nil {
		detail := strings.TrimSpace(stderr.String())
		if detail != "" {
			return fmt.Errorf("codex exec failed: %w: %s", waitErr, detail)
		}
		return fmt.Errorf("codex exec failed: %w", waitErr)
	}
	return nil
}

func scanLines(ctx context.Context, reader io.Reader, lines chan<- string) error {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for scanner.Scan() {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case lines <- scanner.Text():
		}
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil && !errors.Is(err, context.Canceled) {
		return err
	}
	return nil
}

func (e *Exec) env(args ExecArgs) []string {
	values := map[string]string{}
	if e.envOverride != nil {
		for key, value := range e.envOverride {
			values[key] = value
		}
	} else {
		for _, entry := range os.Environ() {
			key, value, ok := strings.Cut(entry, "=")
			if ok {
				values[key] = value
			}
		}
	}
	if values[internalOriginatorEnv] == "" {
		values[internalOriginatorEnv] = goSDKOriginator
	}
	if args.APIKey != "" {
		values["CODEX_API_KEY"] = args.APIKey
	}
	env := make([]string, 0, len(values))
	for key, value := range values {
		env = append(env, key+"="+value)
	}
	return env
}

func cloneStringMap(values map[string]string) map[string]string {
	if values == nil {
		return nil
	}
	out := make(map[string]string, len(values))
	for key, value := range values {
		out[key] = value
	}
	return out
}
