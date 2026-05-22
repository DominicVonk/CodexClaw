package session

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

type Store struct {
	db *sql.DB
}

type Memory struct {
	ID        int64
	ScopeKey  string
	Content   string
	CreatedAt time.Time
}

type Session struct {
	ID                       int64
	ScopeKey                 string
	Name                     string
	ThreadID                 string
	ReasoningEffort          string
	Model                    string
	InputTokens              int64
	OutputTokens             int64
	TotalTokens              int64
	LastInputTokens          int64
	LastOutputTokens         int64
	LastTotalTokens          int64
	LastCompactedTotalTokens int64
	CreatedAt                time.Time
	UpdatedAt                time.Time
}

func Open(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=foreign_keys(1)")
	if err != nil {
		return nil, err
	}
	store := &Store{db: db}
	if err := store.migrate(context.Background()); err != nil {
		_ = db.Close()
		return nil, err
	}
	return store, nil
}

func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

func (s *Store) Active(ctx context.Context, scopeKey string) (Session, bool, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT cs.id, cs.scope_key, cs.name, cs.thread_id, cs.reasoning_effort, cs.model, cs.input_tokens, cs.output_tokens, cs.total_tokens, cs.last_input_tokens, cs.last_output_tokens, cs.last_total_tokens, cs.last_compacted_total_tokens, cs.created_at, cs.updated_at
		FROM active_sessions active
		JOIN chat_sessions cs ON cs.id = active.session_id
		WHERE active.scope_key = ?`, scopeKey)
	return scanOptional(row)
}

func (s *Store) Create(ctx context.Context, scopeKey string, name string, threadID string) (Session, error) {
	name = normalizeName(name)
	if name == "" {
		name = defaultName(time.Now())
	}
	name, err := s.availableName(ctx, scopeKey, name)
	if err != nil {
		return Session{}, err
	}
	now := time.Now().UTC()
	result, err := s.db.ExecContext(ctx, `
		INSERT INTO chat_sessions(scope_key, name, thread_id, reasoning_effort, model, input_tokens, output_tokens, total_tokens, last_input_tokens, last_output_tokens, last_total_tokens, last_compacted_total_tokens, created_at, updated_at)
		VALUES(?, ?, ?, ?, ?, 0, 0, 0, 0, 0, 0, 0, ?, ?)`, scopeKey, name, threadID, "", "", now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano))
	if err != nil {
		return Session{}, err
	}
	id, err := result.LastInsertId()
	if err != nil {
		return Session{}, err
	}
	created := Session{ID: id, ScopeKey: scopeKey, Name: name, ThreadID: threadID, CreatedAt: now, UpdatedAt: now}
	if err := s.SetActive(ctx, scopeKey, id); err != nil {
		return Session{}, err
	}
	return created, nil
}

func (s *Store) SetActive(ctx context.Context, scopeKey string, sessionID int64) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO active_sessions(scope_key, session_id, updated_at)
		VALUES(?, ?, ?)
		ON CONFLICT(scope_key) DO UPDATE SET session_id = excluded.session_id, updated_at = excluded.updated_at`, scopeKey, sessionID, time.Now().UTC().Format(time.RFC3339Nano))
	return err
}

func (s *Store) List(ctx context.Context, scopeKey string) ([]Session, int64, error) {
	activeID := int64(0)
	_ = s.db.QueryRowContext(ctx, `SELECT session_id FROM active_sessions WHERE scope_key = ?`, scopeKey).Scan(&activeID)

	rows, err := s.db.QueryContext(ctx, `
		SELECT id, scope_key, name, thread_id, reasoning_effort, model, input_tokens, output_tokens, total_tokens, last_input_tokens, last_output_tokens, last_total_tokens, last_compacted_total_tokens, created_at, updated_at
		FROM chat_sessions
		WHERE scope_key = ?
		ORDER BY updated_at DESC, id DESC`, scopeKey)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	var sessions []Session
	for rows.Next() {
		session, err := scan(rows)
		if err != nil {
			return nil, 0, err
		}
		sessions = append(sessions, session)
	}
	return sessions, activeID, rows.Err()
}

func (s *Store) Find(ctx context.Context, scopeKey string, selector string) (Session, error) {
	selector = strings.TrimSpace(selector)
	if selector == "" {
		return Session{}, errors.New("session selector is required")
	}
	if id, err := strconv.ParseInt(selector, 10, 64); err == nil {
		row := s.db.QueryRowContext(ctx, `
			SELECT id, scope_key, name, thread_id, reasoning_effort, model, input_tokens, output_tokens, total_tokens, last_input_tokens, last_output_tokens, last_total_tokens, last_compacted_total_tokens, created_at, updated_at
			FROM chat_sessions
			WHERE scope_key = ? AND id = ?`, scopeKey, id)
		session, ok, err := scanOptional(row)
		if err != nil || ok {
			return session, err
		}
	}

	row := s.db.QueryRowContext(ctx, `
		SELECT id, scope_key, name, thread_id, reasoning_effort, model, input_tokens, output_tokens, total_tokens, last_input_tokens, last_output_tokens, last_total_tokens, last_compacted_total_tokens, created_at, updated_at
		FROM chat_sessions
		WHERE scope_key = ? AND name = ?`, scopeKey, selector)
	if session, ok, err := scanOptional(row); err != nil || ok {
		return session, err
	}

	rows, err := s.db.QueryContext(ctx, `
		SELECT id, scope_key, name, thread_id, reasoning_effort, model, input_tokens, output_tokens, total_tokens, last_input_tokens, last_output_tokens, last_total_tokens, last_compacted_total_tokens, created_at, updated_at
		FROM chat_sessions
		WHERE scope_key = ? AND name LIKE ?
		ORDER BY updated_at DESC, id DESC
		LIMIT 2`, scopeKey, selector+"%")
	if err != nil {
		return Session{}, err
	}
	defer rows.Close()

	var matches []Session
	for rows.Next() {
		session, err := scan(rows)
		if err != nil {
			return Session{}, err
		}
		matches = append(matches, session)
	}
	if err := rows.Err(); err != nil {
		return Session{}, err
	}
	if len(matches) == 1 {
		return matches[0], nil
	}
	if len(matches) > 1 {
		return Session{}, fmt.Errorf("session selector %q is ambiguous", selector)
	}
	return Session{}, fmt.Errorf("session %q not found", selector)
}

func (s *Store) Touch(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `UPDATE chat_sessions SET updated_at = ? WHERE id = ?`, time.Now().UTC().Format(time.RFC3339Nano), id)
	return err
}

func (s *Store) migrate(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS chat_sessions (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			scope_key TEXT NOT NULL,
			name TEXT NOT NULL,
			thread_id TEXT NOT NULL,
			reasoning_effort TEXT NOT NULL DEFAULT '',
			model TEXT NOT NULL DEFAULT '',
			input_tokens INTEGER NOT NULL DEFAULT 0,
			output_tokens INTEGER NOT NULL DEFAULT 0,
			total_tokens INTEGER NOT NULL DEFAULT 0,
			last_input_tokens INTEGER NOT NULL DEFAULT 0,
			last_output_tokens INTEGER NOT NULL DEFAULT 0,
			last_total_tokens INTEGER NOT NULL DEFAULT 0,
			last_compacted_total_tokens INTEGER NOT NULL DEFAULT 0,
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL,
			UNIQUE(scope_key, name)
		);
		CREATE TABLE IF NOT EXISTS active_sessions (
			scope_key TEXT PRIMARY KEY,
			session_id INTEGER NOT NULL REFERENCES chat_sessions(id) ON DELETE CASCADE,
			updated_at TEXT NOT NULL
		);
		CREATE INDEX IF NOT EXISTS idx_chat_sessions_scope_updated ON chat_sessions(scope_key, updated_at DESC);
		CREATE TABLE IF NOT EXISTS memories (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			scope_key TEXT NOT NULL,
			content TEXT NOT NULL,
			created_at TEXT NOT NULL
		);
		CREATE INDEX IF NOT EXISTS idx_memories_scope_created ON memories(scope_key, created_at DESC);
	`); err != nil {
		return err
	}
	for _, statement := range []string{
		`ALTER TABLE chat_sessions ADD COLUMN reasoning_effort TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE chat_sessions ADD COLUMN model TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE chat_sessions ADD COLUMN input_tokens INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE chat_sessions ADD COLUMN output_tokens INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE chat_sessions ADD COLUMN total_tokens INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE chat_sessions ADD COLUMN last_input_tokens INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE chat_sessions ADD COLUMN last_output_tokens INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE chat_sessions ADD COLUMN last_total_tokens INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE chat_sessions ADD COLUMN last_compacted_total_tokens INTEGER NOT NULL DEFAULT 0`,
	} {
		if _, alterErr := s.db.ExecContext(ctx, statement); alterErr != nil && !strings.Contains(alterErr.Error(), "duplicate column name") {
			return alterErr
		}
	}
	return nil
}

func (s *Store) UpdateReasoning(ctx context.Context, id int64, effort string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE chat_sessions SET reasoning_effort = ?, updated_at = ? WHERE id = ?`, effort, time.Now().UTC().Format(time.RFC3339Nano), id)
	return err
}

func (s *Store) UpdateModel(ctx context.Context, id int64, model string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE chat_sessions SET model = ?, updated_at = ? WHERE id = ?`, model, time.Now().UTC().Format(time.RFC3339Nano), id)
	return err
}

func (s *Store) UpdateThreadID(ctx context.Context, id int64, threadID string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE chat_sessions SET thread_id = ?, updated_at = ? WHERE id = ?`, threadID, time.Now().UTC().Format(time.RFC3339Nano), id)
	return err
}

func (s *Store) UpdateTokenUsage(ctx context.Context, id int64, inputTokens int64, outputTokens int64, totalTokens int64, lastInputTokens int64, lastOutputTokens int64, lastTotalTokens int64) error {
	_, err := s.db.ExecContext(ctx, `UPDATE chat_sessions SET input_tokens = ?, output_tokens = ?, total_tokens = ?, last_input_tokens = ?, last_output_tokens = ?, last_total_tokens = ?, updated_at = ? WHERE id = ?`, inputTokens, outputTokens, totalTokens, lastInputTokens, lastOutputTokens, lastTotalTokens, time.Now().UTC().Format(time.RFC3339Nano), id)
	return err
}

func (s *Store) MarkCompacted(ctx context.Context, id int64, totalTokens int64) error {
	_, err := s.db.ExecContext(ctx, `UPDATE chat_sessions SET last_compacted_total_tokens = ?, updated_at = ? WHERE id = ?`, totalTokens, time.Now().UTC().Format(time.RFC3339Nano), id)
	return err
}

func (s *Store) AddMemory(ctx context.Context, scopeKey string, content string) (Memory, error) {
	content = strings.TrimSpace(content)
	if content == "" {
		return Memory{}, errors.New("memory content is required")
	}
	now := time.Now().UTC()
	result, err := s.db.ExecContext(ctx, `INSERT INTO memories(scope_key, content, created_at) VALUES(?, ?, ?)`, scopeKey, content, now.Format(time.RFC3339Nano))
	if err != nil {
		return Memory{}, err
	}
	id, err := result.LastInsertId()
	if err != nil {
		return Memory{}, err
	}
	return Memory{ID: id, ScopeKey: scopeKey, Content: content, CreatedAt: now}, nil
}

func (s *Store) ListMemories(ctx context.Context, scopeKey string) ([]Memory, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, scope_key, content, created_at FROM memories WHERE scope_key = ? ORDER BY created_at DESC, id DESC`, scopeKey)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var memories []Memory
	for rows.Next() {
		memory, err := scanMemory(rows)
		if err != nil {
			return nil, err
		}
		memories = append(memories, memory)
	}
	return memories, rows.Err()
}

func (s *Store) DeleteMemory(ctx context.Context, scopeKey string, id int64) (bool, error) {
	result, err := s.db.ExecContext(ctx, `DELETE FROM memories WHERE scope_key = ? AND id = ?`, scopeKey, id)
	if err != nil {
		return false, err
	}
	count, err := result.RowsAffected()
	return count > 0, err
}

func (s *Store) ClearMemories(ctx context.Context, scopeKey string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM memories WHERE scope_key = ?`, scopeKey)
	return err
}

func (s *Store) availableName(ctx context.Context, scopeKey string, base string) (string, error) {
	candidate := base
	for i := 2; ; i++ {
		var exists int
		err := s.db.QueryRowContext(ctx, `SELECT 1 FROM chat_sessions WHERE scope_key = ? AND name = ?`, scopeKey, candidate).Scan(&exists)
		if errors.Is(err, sql.ErrNoRows) {
			return candidate, nil
		}
		if err != nil {
			return "", err
		}
		candidate = fmt.Sprintf("%s-%d", base, i)
	}
}

func scanOptional(row interface{ Scan(dest ...any) error }) (Session, bool, error) {
	session, err := scan(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Session{}, false, nil
	}
	if err != nil {
		return Session{}, false, err
	}
	return session, true, nil
}

func scan(row interface{ Scan(dest ...any) error }) (Session, error) {
	var session Session
	var created string
	var updated string
	if err := row.Scan(&session.ID, &session.ScopeKey, &session.Name, &session.ThreadID, &session.ReasoningEffort, &session.Model, &session.InputTokens, &session.OutputTokens, &session.TotalTokens, &session.LastInputTokens, &session.LastOutputTokens, &session.LastTotalTokens, &session.LastCompactedTotalTokens, &created, &updated); err != nil {
		return Session{}, err
	}
	session.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
	session.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updated)
	return session, nil
}

func scanMemory(row interface{ Scan(dest ...any) error }) (Memory, error) {
	var memory Memory
	var created string
	if err := row.Scan(&memory.ID, &memory.ScopeKey, &memory.Content, &created); err != nil {
		return Memory{}, err
	}
	memory.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
	return memory, nil
}

func normalizeName(name string) string {
	name = strings.TrimSpace(name)
	name = strings.TrimPrefix(name, "/")
	name = strings.Join(strings.Fields(name), "-")
	return name
}

func defaultName(now time.Time) string {
	return "session-" + now.UTC().Format("20060102-150405")
}
