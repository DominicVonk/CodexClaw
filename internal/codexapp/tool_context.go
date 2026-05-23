package codexapp

import "strings"

func commandContext(command string) string {
	command = strings.TrimSpace(command)
	if command == "" || isGenericCommandItem(command) || command == "shell command" {
		return ""
	}
	lower := strings.ToLower(command)
	first := firstShellWord(lower)
	switch {
	case strings.Contains(lower, "--version") || strings.HasPrefix(lower, "which ") || strings.HasPrefix(lower, "command -v ") || strings.HasPrefix(lower, "type "):
		return "checking command availability or version"
	case strings.HasPrefix(lower, "go test"):
		return "running Go tests"
	case strings.HasPrefix(lower, "go build"):
		return "building the Go project"
	case strings.HasPrefix(lower, "go run"):
		return "running the Go service or CLI"
	case strings.HasPrefix(lower, "gofmt") || strings.HasPrefix(lower, "go fmt"):
		return "formatting Go files"
	case strings.HasPrefix(lower, "git status"):
		return "checking repository state"
	case strings.HasPrefix(lower, "git diff"):
		return "reviewing local code changes"
	case strings.HasPrefix(lower, "git add") || strings.HasPrefix(lower, "git commit") || strings.HasPrefix(lower, "git push"):
		return "publishing repository changes"
	case strings.HasPrefix(lower, "gh run") || strings.HasPrefix(lower, "gh pr") || strings.HasPrefix(lower, "gh issue"):
		return "checking GitHub state"
	case strings.HasPrefix(lower, "pm2 "):
		return "managing the running service"
	case first == "rg" || first == "grep":
		return "searching repository text"
	case first == "sed" || first == "cat" || first == "head" || first == "tail" || first == "nl":
		return "reading file content"
	case first == "ls" || first == "find" || first == "fd":
		return "inspecting files and directories"
	case strings.HasPrefix(lower, "mise "):
		return "running through the mise toolchain"
	case strings.HasPrefix(lower, "npm ") || strings.HasPrefix(lower, "pnpm ") || strings.HasPrefix(lower, "yarn "):
		return "running JavaScript project tooling"
	default:
		return "running a shell command for this request"
	}
}

func isGenericCommandItem(itemType string) bool {
	switch itemType {
	case "command_execution", "commandExecution":
		return true
	default:
		return false
	}
}

func firstShellWord(command string) string {
	command = strings.TrimSpace(command)
	if command == "" {
		return ""
	}
	fields := strings.Fields(command)
	if len(fields) == 0 {
		return ""
	}
	return strings.Trim(fields[0], "'\"")
}
