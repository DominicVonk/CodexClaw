package codexsdk

// Codex is the main client for interacting with the Codex agent.
type Codex struct {
	exec    *Exec
	options Options
}

// New creates a Codex SDK client.
func New(options Options) *Codex {
	return &Codex{
		exec:    newExec(options),
		options: options,
	}
}

// StartThread starts a new conversation. The thread ID is populated after the
// first thread.started event.
func (c *Codex) StartThread(options ThreadOptions) *Thread {
	return newThread(c.exec, c.options, options, "")
}

// ResumeThread resumes a persisted conversation by thread ID.
func (c *Codex) ResumeThread(id string, options ThreadOptions) *Thread {
	return newThread(c.exec, c.options, options, id)
}
