package codexsdk

// ConfigValue is a Codex CLI configuration value accepted by --config.
// Supported concrete values are string, finite numeric types, bool, arrays,
// and map/object values. Nil/null values are rejected when serialized.
type ConfigValue any

// ConfigObject contains additional Codex CLI configuration overrides.
// Nested objects are flattened into dotted --config paths.
type ConfigObject map[string]ConfigValue

// Options configures a Codex SDK client.
type Options struct {
	CodexPathOverride string
	BaseURL           string
	APIKey            string
	Config            ConfigObject
	Env               map[string]string
}

// ApprovalMode maps to Codex CLI approval_policy.
type ApprovalMode string

const (
	ApprovalModeNever     ApprovalMode = "never"
	ApprovalModeOnRequest ApprovalMode = "on-request"
	ApprovalModeOnFailure ApprovalMode = "on-failure"
	ApprovalModeUntrusted ApprovalMode = "untrusted"
)

// SandboxMode maps to Codex CLI --sandbox.
type SandboxMode string

const (
	SandboxModeReadOnly         SandboxMode = "read-only"
	SandboxModeWorkspaceWrite   SandboxMode = "workspace-write"
	SandboxModeDangerFullAccess SandboxMode = "danger-full-access"
)

// ModelReasoningEffort maps to Codex CLI model_reasoning_effort.
type ModelReasoningEffort string

const (
	ModelReasoningEffortMinimal ModelReasoningEffort = "minimal"
	ModelReasoningEffortLow     ModelReasoningEffort = "low"
	ModelReasoningEffortMedium  ModelReasoningEffort = "medium"
	ModelReasoningEffortHigh    ModelReasoningEffort = "high"
	ModelReasoningEffortXHigh   ModelReasoningEffort = "xhigh"
)

// WebSearchMode maps to Codex CLI web_search.
type WebSearchMode string

const (
	WebSearchModeDisabled WebSearchMode = "disabled"
	WebSearchModeCached   WebSearchMode = "cached"
	WebSearchModeLive     WebSearchMode = "live"
)

// ThreadOptions configures every turn sent through a Thread.
type ThreadOptions struct {
	Model                 string
	SandboxMode           SandboxMode
	WorkingDirectory      string
	AdditionalDirectories []string
	SkipGitRepoCheck      bool
	ModelReasoningEffort  ModelReasoningEffort
	NetworkAccessEnabled  *bool
	WebSearchMode         WebSearchMode
	WebSearchEnabled      *bool
	ApprovalPolicy        ApprovalMode
}

// TurnOptions configures one run.
type TurnOptions struct {
	OutputSchema any
}

// UserInput is a structured input part accepted by Thread.Run.
type UserInput struct {
	Type string
	Text string
	Path string
}

// TextInput creates a text input part.
func TextInput(text string) UserInput {
	return UserInput{Type: "text", Text: text}
}

// LocalImageInput creates a local image input part.
func LocalImageInput(path string) UserInput {
	return UserInput{Type: "local_image", Path: path}
}
