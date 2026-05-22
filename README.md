# CodexClaw

CodexClaw is a Go daemon that turns Telegram and WhatsApp into chat interfaces for Codex. It is built for private, allowlisted use: send a message from Telegram or WhatsApp, let Codex work in the configured workspace, and get progress plus the final answer back in chat.

## Highlights

- **Go port of the TypeScript Codex SDK** using `codex exec --json`, without `codex app-server`
- **Telegram bot support** with groups, supergroups, and forum topic threads
- **Telegram command menu** registered automatically with the supported slash commands
- **WhatsApp support** through `whatsmeow` with terminal QR login
- **Sender allowlist** for Telegram user IDs and WhatsApp phone/JID senders
- **Persistent sessions** with `/new`, `/session`, and `codex exec resume`
- **Model and reasoning controls** per session or globally with `/model` and `/reasoning`
- **Memory** per chat scope with `/remember`, `/memory`, and `/forget`
- **Skills** with `$skill-name`, `/skills`, `$memory`, `$skill-creator`, and the built-in `$skills` dictionary
- **Attachments** for Telegram/WhatsApp images and documents
- **Tool progress messages** while Codex runs commands, edits files, searches, compacts, and calls tools
- **Pure-Go SQLite** via `modernc.org/sqlite`, so CGO is not required

## Requirements

- Go 1.25+
- `mise` is optional but supported via `.mise.toml`
- Codex CLI installed and authenticated
- Telegram bot token if Telegram is enabled
- A WhatsApp account for QR login if WhatsApp is enabled

## Quick Start

```sh
cp config.example.yaml ~/.codex-claw/config.yml
cp .env.example ~/.codex-claw/.env
```

Edit `~/.codex-claw/config.yml`:

- Set `telegram.token` or set `TELEGRAM_BOT_TOKEN` in `~/.codex-claw/.env`
- Add your Telegram sender ID as `telegram:<user_id>`
- Add your WhatsApp sender as `whatsapp:<phone_number>` or `whatsapp:<jid>`
- Choose `service.mode: telegram`, `whatsapp`, or `both`

Run locally:

```sh
mise exec -- go run ./cmd/codexclaw serve
```

For WhatsApp, scan the QR code printed in the terminal or service logs. The authenticated WhatsApp session defaults to `./whatsapp-session/whatsapp.db`. If QR pairing is unreliable from service logs, use phone-number pairing:

```sh
pm2 stop codex-claw
mise exec -- go run ./cmd/codexclaw whatsapp-login -phone 31612345678
pm2 restart codex-claw --update-env
```

Enter the printed pairing code in WhatsApp under Linked devices > Link with phone number instead.

## Service Mode

```yaml
service:
  mode: both
```

Valid modes:

- `telegram`
- `whatsapp`
- `both`

The mode selects which transports run. Each transport also has its own `enabled` flag. You can override the mode with:

```env
CODEXCLAW_SERVICE_MODE=telegram
```

## Configuration

CodexClaw loads `.env` from `$HOME/.codex-claw` and then the current directory. It then searches for config files in this order:

```text
./config.yml
./config.yaml
$HOME/.codex-claw/config.yml
$HOME/.codex-claw/config.yaml
```

YAML values can reference environment variables with `${VAR}`. Common settings can also be overridden with `CODEXCLAW_*` variables.

Default runtime paths:

```text
$HOME/.codex-claw/config.yml
$HOME/.codex-claw/.env
$HOME/.codex-claw/sessions.db
$HOME/.codex-claw/media
./whatsapp-session/whatsapp.db
```

## Allowlist

When `allowlist.enabled` is true, CodexClaw ignores messages unless the sender is listed.

```yaml
allowlist:
  enabled: true
  entries:
    - telegram:123456789
    - whatsapp:31612345678
    - whatsapp:31612345678@s.whatsapp.net
```

Telegram authorization uses `message.from.id`. WhatsApp authorization uses the sender phone/user JID. Sessions still map to the chat destination, so Telegram topics, WhatsApp DMs, and WhatsApp groups each keep separate Codex sessions.

## Chat Commands

```text
/new [name]
/session
/session <id|name>
/status
/model [model-name|default] [--global]
/reasoning [low|medium|high|xhigh|default] [--global]
/skills
/remember <text>
/memory
/forget <id|all>
```

Session commands:

- `/new [name]` creates a fresh Codex thread and makes it active.
- `/session` lists stored sessions for the current chat scope.
- `/session <id|name>` switches to a stored session and resumes its Codex thread.

Status, model, and reasoning:

- `/status` shows active session, thread ID, model, cumulative token usage, last-turn token usage, reasoning level, and compaction settings.
- `/model <model-name>` changes the model for the active session.
- `/model <model-name> --global` updates `codex.model` in the loaded config file and persists across restarts.
- `/model default` resets the active session to the config default.
- `/reasoning <level>` changes reasoning for the active session.
- `/reasoning <level> --global` updates `codex.effort` in the loaded config file and persists across restarts.
- `/reasoning default` resets the active session to the config default.

Memory:

- `/remember <text>` saves persistent memory for the current chat scope.
- `/memory` lists saved memory.
- `/forget <id|all>` deletes one memory item or clears all memory for the current scope.
- CodexClaw automatically includes a tiny relevant memory set when the message matches saved memory. Add `$memory` or `$memories` for manual memory context, or `$memory all` for every memory.

Skills:

- `/skills` lists available skills.
- `$skill-name` attaches a matching Codex skill to the next turn.
- `$skills` and `$skill-dictionary` inject a compact dictionary of available skill names into the next turn.
- `$memory` and `$memories` inject saved memories for the current chat.
- `$skill-creator` injects a compact dictionary entry for creating or updating Codex skills.

## Skills.sh And Scaffolding

CodexClaw includes mise tasks for skills.sh discovery/installation and local skill/plugin creation.

```sh
mise run skills:find -- github
mise run skills:add -- vercel-labs/agent-skills
mise run skills:list
mise run skills:update
```

`skills:add` runs `npx --yes skills add` with telemetry disabled by default and installs globally for the Codex agent. Override the target with `SKILLS_AGENT=<agent>`.

Local scaffolds:

```sh
mise run skill:new -- repo-maintainer "Repository maintenance workflow"
mise run plugin:new -- repo-tools "Repository tools for Codex"
```

New skills are created under `./skills/<name>/SKILL.md`. New plugins are created under `./plugins/<name>/.codex-plugin/plugin.json` with an empty `skills/` directory ready for plugin-owned skills.

## Attachments

Telegram photos/documents and WhatsApp images/documents are downloaded to `media.dir`.

- Images are sent to Codex CLI with `--image`.
- Documents are saved locally and included in the text input as filesystem paths so Codex can inspect them with workspace tools.

## Auto-Compaction

The `codex exec` SDK backend does not expose an explicit compaction endpoint. When `sessions.auto_compact` is true and the threshold is reached, CodexClaw records the threshold as handled and reports that explicit compaction is unavailable for this backend.

```yaml
sessions:
  auto_compact: true
  auto_compact_after_tokens: 120000
```

## Running With PM2

```sh
pm2 start /usr/bin/mise --name codex-claw --cwd "$PWD" -- exec -- go run ./cmd/codexclaw serve
pm2 status codex-claw
pm2 logs codex-claw
```

## Development

```sh
mise install
mise exec -- gofmt -w ./cmd ./internal
mise exec -- env CGO_ENABLED=0 go test ./...
mise exec -- env CGO_ENABLED=0 go build ./...
```

## CI Builds

GitHub Actions builds `0.0.0-alpha.1` artifacts for:

- `linux/amd64`
- `linux/arm64`
- `darwin/amd64`
- `darwin/arm64`
- `windows/amd64`
- `windows/arm64`

Each artifact is named `codexclaw_0.0.0-alpha.1_<os>_<arch>` and includes a SHA256 file.

## Security Notes

CodexClaw is intended for private, allowlisted operation. Do not commit real `config.yml`, `.env`, WhatsApp sessions, or runtime databases. The included `.gitignore` excludes those files by default.

## Disclaimer

CodexClaw is an independent, free open-source project and is not affiliated, associated, authorized, endorsed by, or in any way officially connected with OpenAI, OpenClaw, or any of their subsidiaries or affiliates. "OpenAI", "Codex", and "OpenClaw" are trademarks of their respective owners.

## License

MIT License. You can use, copy, modify, merge, publish, distribute, sublicense, and sell copies under the MIT terms. Published versions of CodexClaw that are released under MIT remain available under those MIT terms for people who receive them; later project changes do not take those granted rights away from those copies.
