package main

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

type Database struct {
	conn *sql.DB
}

func OpenDatabase(path string) (*Database, error) {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create data dir: %w", err)
	}

	conn, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}

	if _, err := conn.Exec(`PRAGMA journal_mode = WAL;`); err != nil {
		return nil, fmt.Errorf("set wal: %w", err)
	}

	if err := initSchema(conn); err != nil {
		return nil, err
	}

	return &Database{conn: conn}, nil
}

func (db *Database) Close() error {
	return db.conn.Close()
}

func initSchema(conn *sql.DB) error {
	_, err := conn.Exec(`
    CREATE TABLE IF NOT EXISTS requests (
      id TEXT PRIMARY KEY,
      session_id TEXT NOT NULL,
      provider TEXT NOT NULL,
      model TEXT,
      stream INTEGER NOT NULL,
      path TEXT NOT NULL,
      user TEXT,
      temperature REAL,
      top_p REAL,
      max_tokens INTEGER,
      status_code INTEGER,
      created_at INTEGER NOT NULL,
      completed_at INTEGER
    );

    CREATE TABLE IF NOT EXISTS messages (
      id INTEGER PRIMARY KEY AUTOINCREMENT,
      request_id TEXT NOT NULL,
      role TEXT NOT NULL,
      content TEXT NOT NULL,
      name TEXT,
      created_at INTEGER NOT NULL,
      FOREIGN KEY (request_id) REFERENCES requests(id)
    );

    CREATE TABLE IF NOT EXISTS responses (
      id INTEGER PRIMARY KEY AUTOINCREMENT,
      request_id TEXT NOT NULL,
      seq INTEGER NOT NULL,
      delta TEXT NOT NULL,
      created_at INTEGER NOT NULL,
      FOREIGN KEY (request_id) REFERENCES requests(id)
    );

    CREATE TABLE IF NOT EXISTS usage (
      request_id TEXT PRIMARY KEY,
      prompt_tokens INTEGER,
      completion_tokens INTEGER,
      total_tokens INTEGER,
      FOREIGN KEY (request_id) REFERENCES requests(id)
    );

    CREATE TABLE IF NOT EXISTS settings (
      key TEXT PRIMARY KEY,
      value TEXT NOT NULL,
      updated_at INTEGER NOT NULL
    );
  `)

	if err != nil {
		return fmt.Errorf("init schema: %w", err)
	}

	addColumnIfMissing(conn, "usage", "cache_read_tokens", "INTEGER NOT NULL DEFAULT 0")
	addColumnIfMissing(conn, "responses", "thinking", "TEXT NOT NULL DEFAULT ''")
	return nil
}

func addColumnIfMissing(conn *sql.DB, table, column, def string) {
	_, _ = conn.Exec(fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s %s;", table, column, def))
}

func (db *Database) InsertRequest(req RequestRecord) error {
	_, err := db.conn.Exec(
		`INSERT INTO requests (id, session_id, provider, model, stream, path, user, temperature, top_p, max_tokens, created_at)
     VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);`,
		req.ID,
		req.SessionID,
		req.Provider,
		req.Model,
		boolToInt(req.Stream),
		req.Path,
		req.User,
		req.Temperature,
		req.TopP,
		req.MaxTokens,
		req.CreatedAt.UnixMilli(),
	)
	if err != nil {
		return fmt.Errorf("insert request: %w", err)
	}
	return nil
}

func (db *Database) InsertMessage(message MessageRecord) error {
	_, err := db.conn.Exec(
		`INSERT INTO messages (request_id, role, content, name, created_at) VALUES (?, ?, ?, ?, ?);`,
		message.RequestID,
		message.Role,
		message.Content,
		message.Name,
		message.CreatedAt.UnixMilli(),
	)
	if err != nil {
		return fmt.Errorf("insert message: %w", err)
	}
	return nil
}

func (db *Database) InsertResponseDelta(delta ResponseDelta) error {
	_, err := db.conn.Exec(
		`INSERT INTO responses (request_id, seq, delta, thinking, created_at) VALUES (?, ?, ?, ?, ?);`,
		delta.RequestID,
		delta.Seq,
		delta.Delta,
		delta.Thinking,
		delta.CreatedAt.UnixMilli(),
	)
	if err != nil {
		return fmt.Errorf("insert response delta: %w", err)
	}
	return nil
}

func (db *Database) UpsertUsage(usage UsageRecord) error {
	_, err := db.conn.Exec(
		`INSERT INTO usage (request_id, prompt_tokens, completion_tokens, total_tokens, cache_read_tokens)
     VALUES (?, ?, ?, ?, ?)
     ON CONFLICT(request_id) DO UPDATE SET
       prompt_tokens = excluded.prompt_tokens,
       completion_tokens = excluded.completion_tokens,
       total_tokens = excluded.total_tokens,
       cache_read_tokens = excluded.cache_read_tokens;`,
		usage.RequestID,
		usage.PromptTokens,
		usage.CompletionTokens,
		usage.TotalTokens,
		usage.CacheReadTokens,
	)
	if err != nil {
		return fmt.Errorf("upsert usage: %w", err)
	}
	return nil
}

func (db *Database) CompleteRequest(id string, statusCode int) error {
	_, err := db.conn.Exec(
		`UPDATE requests SET status_code = ?, completed_at = ? WHERE id = ?;`,
		statusCode,
		time.Now().UnixMilli(),
		id,
	)
	if err != nil {
		return fmt.Errorf("complete request: %w", err)
	}
	return nil
}

func (db *Database) GetSetting(key string) (string, bool, error) {
	row := db.conn.QueryRow(`SELECT value FROM settings WHERE key = ?`, key)
	var value string
	if err := row.Scan(&value); err != nil {
		if err == sql.ErrNoRows {
			return "", false, nil
		}
		return "", false, fmt.Errorf("get setting: %w", err)
	}
	return value, true, nil
}

func (db *Database) SetSetting(key string, value string) error {
	_, err := db.conn.Exec(
		`INSERT INTO settings (key, value, updated_at)
     VALUES (?, ?, ?)
     ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at;`,
		key,
		value,
		time.Now().UnixMilli(),
	)
	if err != nil {
		return fmt.Errorf("set setting: %w", err)
	}
	return nil
}

func boolToInt(value bool) int {
	if value {
		return 1
	}
	return 0
}
