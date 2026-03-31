package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// ---- View types ----

type RequestViewRow struct {
	ID               string   `json:"id"`
	SessionID        string   `json:"sessionId"`
	Provider         string   `json:"provider"`
	Model            string   `json:"model"`
	Stream           bool     `json:"stream"`
	Path             string   `json:"path"`
	Temperature      *float64 `json:"temperature"`
	TopP             *float64 `json:"topP"`
	MaxTokens        *int     `json:"maxTokens"`
	StatusCode       int      `json:"statusCode"`
	CreatedAt        int64    `json:"createdAt"`
	CompletedAt      *int64   `json:"completedAt"`
	DurationMs       int64    `json:"durationMs"`
	PromptTokens     int      `json:"promptTokens"`
	CompletionTokens int      `json:"completionTokens"`
	TotalTokens      int      `json:"totalTokens"`
	CacheReadTokens  int      `json:"cacheReadTokens"`
}

type MessageView struct {
	Role    string `json:"role"`
	Content string `json:"content"`
	Name    string `json:"name"`
}

type ToolView struct {
	Type        string `json:"type"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Parameters  string `json:"parameters"`
}

type RequestDetailView struct {
	Request           RequestViewRow `json:"request"`
	Messages          []MessageView  `json:"messages"`
	Tools             []ToolView     `json:"tools"`
	RawBody           string         `json:"rawBody"`
	RawResponse       string         `json:"rawResponse"`
	RawClientResponse string         `json:"rawClientResponse"`
	Response          string         `json:"response"`
	Thinking          string         `json:"thinking"`
}

type SessionView struct {
	SessionID    string `json:"sessionId"`
	RequestCount int    `json:"requestCount"`
	LastActivity int64  `json:"lastActivity"`
	TotalTokens  int    `json:"totalTokens"`
	Providers    string `json:"providers"`
}

// ---- DB query methods ----

func (db *Database) ListSessions(limit int) ([]SessionView, error) {
	rows, err := db.conn.Query(`
		SELECT
			r.session_id,
			COUNT(*) as request_count,
			MAX(COALESCE(r.completed_at, r.created_at)) as last_activity,
			COALESCE(SUM(u.total_tokens), 0) as total_tokens,
			GROUP_CONCAT(DISTINCT r.provider) as providers
		FROM requests r
		LEFT JOIN usage u ON u.request_id = r.id
		GROUP BY r.session_id
		ORDER BY last_activity DESC
		LIMIT ?
	`, limit)
	if err != nil {
		return nil, fmt.Errorf("list sessions: %w", err)
	}
	defer rows.Close()

	var result []SessionView
	for rows.Next() {
		var s SessionView
		if err := rows.Scan(&s.SessionID, &s.RequestCount, &s.LastActivity, &s.TotalTokens, &s.Providers); err != nil {
			return nil, fmt.Errorf("scan session: %w", err)
		}
		result = append(result, s)
	}
	return result, nil
}

func (db *Database) ListSessionDetails(sessionID string) ([]RequestDetailView, error) {
	rows, err := db.conn.Query(`
		SELECT
			r.id, r.session_id, r.provider, COALESCE(r.model, ''), r.stream, r.path,
			r.temperature, r.top_p, r.max_tokens,
			COALESCE(r.status_code, 0), r.created_at, r.completed_at,
			COALESCE(u.prompt_tokens, 0), COALESCE(u.completion_tokens, 0),
			COALESCE(u.total_tokens, 0), COALESCE(u.cache_read_tokens, 0),
			COALESCE(r.raw_body, ''), COALESCE(r.raw_response, ''), COALESCE(r.raw_client_response, '')
		FROM requests r
		LEFT JOIN usage u ON u.request_id = r.id
		WHERE r.session_id = ?
		ORDER BY r.created_at ASC
	`, sessionID)
	if err != nil {
		return nil, fmt.Errorf("list session requests: %w", err)
	}
	defer rows.Close()

	type rowWithRaw struct {
		row               RequestViewRow
		rawBody           string
		rawResponse       string
		rawClientResponse string
	}
	var requestRows []rowWithRaw
	for rows.Next() {
		var row RequestViewRow
		var stream int
		var completedAt sql.NullInt64
		var temperature, topP sql.NullFloat64
		var maxTokens sql.NullInt64
		var rawBody, rawResponse, rawClientResponse string
		if err := rows.Scan(
			&row.ID, &row.SessionID, &row.Provider, &row.Model, &stream, &row.Path,
			&temperature, &topP, &maxTokens,
			&row.StatusCode, &row.CreatedAt, &completedAt,
			&row.PromptTokens, &row.CompletionTokens, &row.TotalTokens, &row.CacheReadTokens,
			&rawBody, &rawResponse, &rawClientResponse,
		); err != nil {
			return nil, fmt.Errorf("scan session request: %w", err)
		}
		row.Stream = stream == 1
		if completedAt.Valid {
			v := completedAt.Int64
			row.CompletedAt = &v
			row.DurationMs = v - row.CreatedAt
		}
		if temperature.Valid {
			row.Temperature = &temperature.Float64
		}
		if topP.Valid {
			row.TopP = &topP.Float64
		}
		if maxTokens.Valid {
			v := int(maxTokens.Int64)
			row.MaxTokens = &v
		}
		requestRows = append(requestRows, rowWithRaw{row: row, rawBody: rawBody, rawResponse: rawResponse, rawClientResponse: rawClientResponse})
	}
	rows.Close()

	var details []RequestDetailView
	for _, rr := range requestRows {
		req := rr.row
		msgRows, err := db.conn.Query(
			`SELECT role, content, COALESCE(name, '') FROM messages WHERE request_id = ? ORDER BY id ASC`,
			req.ID,
		)
		if err != nil {
			return nil, fmt.Errorf("get messages: %w", err)
		}
		var messages []MessageView
		for msgRows.Next() {
			var m MessageView
			if err := msgRows.Scan(&m.Role, &m.Content, &m.Name); err != nil {
				msgRows.Close()
				return nil, fmt.Errorf("scan message: %w", err)
			}
			messages = append(messages, m)
		}
		msgRows.Close()

		respRows, err := db.conn.Query(
			`SELECT delta, thinking FROM responses WHERE request_id = ? ORDER BY seq ASC`,
			req.ID,
		)
		if err != nil {
			return nil, fmt.Errorf("get responses: %w", err)
		}
		var sb, thinkingSb strings.Builder
		for respRows.Next() {
			var delta, thinking string
			if err := respRows.Scan(&delta, &thinking); err != nil {
				respRows.Close()
				return nil, fmt.Errorf("scan response: %w", err)
			}
			sb.WriteString(delta)
			if thinking != "" {
				thinkingSb.WriteString(thinking)
			}
		}
		respRows.Close()

		toolRows, err := db.conn.Query(
			`SELECT type, name, description, parameters FROM tools WHERE request_id = ? ORDER BY id ASC`,
			req.ID,
		)
		if err != nil {
			return nil, fmt.Errorf("get tools: %w", err)
		}
		var tools []ToolView
		for toolRows.Next() {
			var t ToolView
			if err := toolRows.Scan(&t.Type, &t.Name, &t.Description, &t.Parameters); err != nil {
				toolRows.Close()
				return nil, fmt.Errorf("scan tool: %w", err)
			}
			tools = append(tools, t)
		}
		toolRows.Close()

		details = append(details, RequestDetailView{
			Request:           req,
			Messages:          messages,
			Tools:             tools,
			RawBody:           rr.rawBody,
			RawResponse:       rr.rawResponse,
			RawClientResponse: rr.rawClientResponse,
			Response:          sb.String(),
			Thinking:          thinkingSb.String(),
		})
	}
	return details, nil
}

func (db *Database) ListRequestViews(limit int) ([]RequestViewRow, error) {
	rows, err := db.conn.Query(`
		SELECT
			r.id, r.session_id, r.provider, COALESCE(r.model, ''), r.stream, r.path,
			r.temperature, r.top_p, r.max_tokens,
			COALESCE(r.status_code, 0), r.created_at, r.completed_at,
			COALESCE(u.prompt_tokens, 0), COALESCE(u.completion_tokens, 0),
			COALESCE(u.total_tokens, 0), COALESCE(u.cache_read_tokens, 0)
		FROM requests r
		LEFT JOIN usage u ON u.request_id = r.id
		ORDER BY r.created_at DESC
		LIMIT ?
	`, limit)
	if err != nil {
		return nil, fmt.Errorf("list request views: %w", err)
	}
	defer rows.Close()

	var result []RequestViewRow
	for rows.Next() {
		var row RequestViewRow
		var stream int
		var completedAt sql.NullInt64
		var temperature, topP sql.NullFloat64
		var maxTokens sql.NullInt64
		if err := rows.Scan(
			&row.ID, &row.SessionID, &row.Provider, &row.Model, &stream, &row.Path,
			&temperature, &topP, &maxTokens,
			&row.StatusCode, &row.CreatedAt, &completedAt,
			&row.PromptTokens, &row.CompletionTokens, &row.TotalTokens, &row.CacheReadTokens,
		); err != nil {
			return nil, fmt.Errorf("scan request view: %w", err)
		}
		row.Stream = stream == 1
		if completedAt.Valid {
			v := completedAt.Int64
			row.CompletedAt = &v
			row.DurationMs = v - row.CreatedAt
		}
		if temperature.Valid {
			row.Temperature = &temperature.Float64
		}
		if topP.Valid {
			row.TopP = &topP.Float64
		}
		if maxTokens.Valid {
			v := int(maxTokens.Int64)
			row.MaxTokens = &v
		}
		result = append(result, row)
	}
	return result, nil
}

func (db *Database) GetRequestViewDetail(id string) (*RequestDetailView, error) {
	var row RequestViewRow
	var stream int
	var completedAt sql.NullInt64

	var temperature, topP sql.NullFloat64
	var maxTokens sql.NullInt64
	var rawBody, rawResponse, rawClientResponse string

	err := db.conn.QueryRow(`
		SELECT
			r.id, r.session_id, r.provider, COALESCE(r.model, ''), r.stream, r.path,
			r.temperature, r.top_p, r.max_tokens,
			COALESCE(r.status_code, 0), r.created_at, r.completed_at,
			COALESCE(u.prompt_tokens, 0), COALESCE(u.completion_tokens, 0),
			COALESCE(u.total_tokens, 0), COALESCE(u.cache_read_tokens, 0),
			COALESCE(r.raw_body, ''), COALESCE(r.raw_response, ''), COALESCE(r.raw_client_response, '')
		FROM requests r
		LEFT JOIN usage u ON u.request_id = r.id
		WHERE r.id = ?
	`, id).Scan(
		&row.ID, &row.SessionID, &row.Provider, &row.Model, &stream, &row.Path,
		&temperature, &topP, &maxTokens,
		&row.StatusCode, &row.CreatedAt, &completedAt,
		&row.PromptTokens, &row.CompletionTokens, &row.TotalTokens, &row.CacheReadTokens,
		&rawBody, &rawResponse, &rawClientResponse,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get request view detail: %w", err)
	}

	row.Stream = stream == 1
	if completedAt.Valid {
		v := completedAt.Int64
		row.CompletedAt = &v
		row.DurationMs = v - row.CreatedAt
	}
	if temperature.Valid {
		row.Temperature = &temperature.Float64
	}
	if topP.Valid {
		row.TopP = &topP.Float64
	}
	if maxTokens.Valid {
		v := int(maxTokens.Int64)
		row.MaxTokens = &v
	}

	msgRows, err := db.conn.Query(
		`SELECT role, content, COALESCE(name, '') FROM messages WHERE request_id = ? ORDER BY id ASC`,
		id,
	)
	if err != nil {
		return nil, fmt.Errorf("get messages: %w", err)
	}
	defer msgRows.Close()

	var messages []MessageView
	for msgRows.Next() {
		var m MessageView
		if err := msgRows.Scan(&m.Role, &m.Content, &m.Name); err != nil {
			return nil, fmt.Errorf("scan message: %w", err)
		}
		messages = append(messages, m)
	}

	respRows, err := db.conn.Query(
		`SELECT delta, thinking FROM responses WHERE request_id = ? ORDER BY seq ASC`,
		id,
	)
	if err != nil {
		return nil, fmt.Errorf("get responses: %w", err)
	}
	defer respRows.Close()

	var sb, thinkingSb strings.Builder
	for respRows.Next() {
		var delta, thinking string
		if err := respRows.Scan(&delta, &thinking); err != nil {
			return nil, fmt.Errorf("scan response delta: %w", err)
		}
		sb.WriteString(delta)
		if thinking != "" {
			thinkingSb.WriteString(thinking)
		}
	}

	toolRows, err := db.conn.Query(
		`SELECT type, name, description, parameters FROM tools WHERE request_id = ? ORDER BY id ASC`,
		id,
	)
	if err != nil {
		return nil, fmt.Errorf("get tools: %w", err)
	}
	defer toolRows.Close()

	var tools []ToolView
	for toolRows.Next() {
		var t ToolView
		if err := toolRows.Scan(&t.Type, &t.Name, &t.Description, &t.Parameters); err != nil {
			return nil, fmt.Errorf("scan tool: %w", err)
		}
		tools = append(tools, t)
	}

	return &RequestDetailView{
		Request:           row,
		Messages:          messages,
		Tools:             tools,
		RawBody:           rawBody,
		RawResponse:       rawResponse,
		RawClientResponse: rawClientResponse,
		Response:          sb.String(),
		Thinking:          thinkingSb.String(),
	}, nil
}

// ---- HTTP handlers ----

func (s *ProxyServer) handleViewerPage(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(viewerTemplate))
}

func (s *ProxyServer) handleViewerAPISessions(w http.ResponseWriter) {
	sessions, err := s.db.ListSessions(200)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if sessions == nil {
		sessions = []SessionView{}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(sessions)
}

func (s *ProxyServer) handleViewerAPISessionDetail(w http.ResponseWriter, r *http.Request, sessionID string) {
	details, err := s.db.ListSessionDetails(sessionID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if details == nil {
		details = []RequestDetailView{}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(details)
}

func (s *ProxyServer) handleViewerAPIRequests(w http.ResponseWriter) {
	rows, err := s.db.ListRequestViews(200)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if rows == nil {
		rows = []RequestViewRow{}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(rows)
}

func (s *ProxyServer) handleViewerAPIDetail(w http.ResponseWriter, r *http.Request, id string) {
	detail, err := s.db.GetRequestViewDetail(id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if detail == nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(detail)
}

func (s *ProxyServer) handleViewerAPIDeleteSession(w http.ResponseWriter, sessionID string) {
	if err := s.db.DeleteSession(sessionID); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---- HTML template ----
// JS avoids backtick template literals to allow embedding in a Go raw string.

const viewerTemplate = `<!DOCTYPE html>
<html lang="zh-TW">
<head>
  <meta charset="UTF-8">
  <title>Proxy Viewer</title>
  <style>
    *, *::before, *::after { box-sizing: border-box; margin: 0; padding: 0; }

    :root {
      --bg:       #0d1117;
      --bg2:      #161b22;
      --bg3:      #1c2128;
      --border:   #30363d;
      --border2:  #21262d;
      --text:     #e6edf3;
      --muted:    #8b949e;
      --dim:      #6e7681;
      --blue:     #388bfd;
      --green:    #3fb950;
      --yellow:   #d29922;
      --red:      #f85149;
      --purple:   #d2a8ff;
    }

    html, body { height: 100%; }
    body {
      font-family: 'JetBrains Mono', 'Fira Code', ui-monospace, monospace;
      background: var(--bg); color: var(--text); font-size: 13px;
      display: flex; flex-direction: column;
    }

    /* ── Header ── */
    #header {
      flex-shrink: 0;
      display: flex; align-items: center; gap: 10px;
      padding: 8px 16px;
      background: var(--bg2); border-bottom: 1px solid var(--border);
      height: 44px;
    }
    #header h1 { font-size: 14px; color: var(--text); letter-spacing: 0.3px; }
    .hdot { color: var(--muted); }

    button {
      padding: 3px 12px; border-radius: 6px;
      border: 1px solid var(--border); background: var(--bg3);
      color: var(--text); cursor: pointer; font-family: inherit; font-size: 12px;
      transition: background 0.1s;
    }
    button:hover { background: var(--border); }
    button.active { border-color: var(--blue); color: var(--blue); }
    .ml-auto { margin-left: auto; }

    /* ── Body layout ── */
    #body {
      flex: 1; display: flex; overflow: hidden;
    }

    /* ── Sidebar ── */
    #sidebar {
      width: 260px; min-width: 200px; flex-shrink: 0;
      display: flex; flex-direction: column;
      border-right: 1px solid var(--border);
      background: var(--bg);
      overflow: hidden;
      transition: width 0.2s ease, min-width 0.2s ease;
    }
    #sidebar.collapsed {
      width: 40px; min-width: 40px;
    }
    #sidebar.collapsed #session-list,
    #sidebar.collapsed #session-count-sidebar { display: none; }
    #sidebar.collapsed #sidebar-header .sidebar-title { display: none; }

    #sidebar-header {
      padding: 10px 14px;
      background: var(--bg2); border-bottom: 1px solid var(--border);
      font-size: 11px; font-weight: 700; text-transform: uppercase;
      letter-spacing: 0.8px; color: var(--muted);
      flex-shrink: 0;
      display: flex; align-items: center; justify-content: space-between;
    }
    #sidebar-toggle {
      background: none; border: none; color: var(--muted); cursor: pointer;
      padding: 4px 6px; border-radius: 4px; line-height: 0;
      flex-shrink: 0; display: flex; align-items: center;
      transition: color 0.15s, background 0.15s;
    }
    #sidebar-toggle:hover { color: var(--fg); background: var(--bg3); }
    #sidebar-toggle svg { display: block; transition: transform 0.2s ease; }
    #sidebar.collapsed #sidebar-toggle svg { transform: rotate(180deg); }

    #session-list { flex: 1; overflow-y: auto; }

    .session-item {
      padding: 11px 14px; border-bottom: 1px solid var(--border2);
      cursor: pointer; border-left: 3px solid transparent;
      transition: background 0.1s; position: relative;
    }
    .session-item:hover { background: var(--bg3); }
    .session-item.active {
      background: #1c2b3a; border-left-color: var(--blue);
    }
    /* ── Session bar (fixed top of main) ── */
    #session-bar {
      position: sticky; top: 0; z-index: 10;
      display: flex; align-items: center; gap: 10px;
      padding: 8px 20px;
      background: var(--bg2); border-bottom: 1px solid var(--border);
      flex-shrink: 0;
    }
    #session-bar-id {
      font-size: 12px; color: var(--muted); font-family: monospace;
      white-space: nowrap; overflow: hidden; text-overflow: ellipsis; flex: 1;
    }
    #session-bar-meta {
      font-size: 11px; color: var(--dim); white-space: nowrap;
    }
    .session-bar-dot {
      position: relative;
    }
    .session-bar-dot-btn {
      background: none; border: none; color: var(--muted); cursor: pointer;
      padding: 4px 7px; border-radius: 4px; font-size: 16px; line-height: 1;
      display: flex; align-items: center; letter-spacing: 1px;
      transition: color 0.15s, background 0.15s;
    }
    .session-bar-dot-btn:hover { color: var(--fg); background: var(--bg3); }
    .session-bar-menu {
      display: none; position: absolute; right: 0; top: calc(100% + 4px);
      background: var(--bg2); border: 1px solid var(--border);
      border-radius: 6px; box-shadow: 0 4px 16px rgba(0,0,0,0.4);
      min-width: 140px; z-index: 100; overflow: hidden;
    }
    .session-bar-menu.open { display: block; }
    .session-bar-menu-item {
      padding: 9px 14px; font-size: 12px; cursor: pointer;
      display: flex; align-items: center; gap: 8px;
      transition: background 0.1s;
    }
    .session-bar-menu-item:hover { background: var(--bg3); }
    .session-bar-menu-item.danger { color: var(--red); }
    .session-bar-menu-item.danger:hover { background: rgba(255,80,80,0.08); }
    .session-id {
      font-size: 12px; color: var(--text);
      white-space: nowrap; overflow: hidden; text-overflow: ellipsis;
    }
    .session-meta {
      margin-top: 5px; font-size: 11px; color: var(--dim);
      display: flex; gap: 8px; align-items: center; flex-wrap: wrap;
    }
    .session-provider {
      font-size: 10px; font-weight: 700;
      padding: 1px 5px; border-radius: 3px;
    }
    .p-openai    { background: #0d2235; color: var(--blue); }
    .p-gemini    { background: #1a2c1a; color: var(--green); }
    .p-anthropic { background: #2c1a2c; color: var(--purple); }
    .p-copilot   { background: #1a1e2c; color: #79c0ff; }
    .p-default   { background: var(--bg3); color: var(--muted); }

    /* ── Main content ── */
    #main {
      flex: 1; overflow-y: auto;
      display: flex; flex-direction: column;
    }

    #main-empty {
      flex: 1; display: flex; flex-direction: column;
      align-items: center; justify-content: center;
      color: var(--muted); gap: 10px;
    }
    #main-empty .icon { font-size: 40px; opacity: 0.3; }
    #main-empty p { font-size: 13px; }

    /* ── Request block ── */
    .req-block {
      border-bottom: 1px solid var(--border);
    }

    .req-header {
      display: flex; align-items: center; gap: 10px; flex-wrap: wrap;
      padding: 9px 24px;
      background: var(--bg2); border-bottom: 1px solid var(--border2);
      font-size: 12px; color: var(--muted);
      position: sticky; top: 0; z-index: 5;
    }
    .req-provider {
      font-size: 11px; font-weight: 700;
      padding: 2px 7px; border-radius: 4px;
    }
    .req-model { color: var(--text); font-size: 12px; }
    .req-sep { color: var(--border); }
    .status-ok  { color: var(--green); }
    .status-err { color: var(--red); }
    .status-pending { color: var(--yellow); }
    .req-tokens { color: var(--muted); }
    .req-time { color: var(--dim); font-size: 11px; margin-left: auto; }

    /* ── Conversation area ── */
    .conv { padding: 24px 32px; display: flex; flex-direction: column; gap: 20px; }

    /* ── Message ── */
    .msg { display: flex; flex-direction: column; gap: 6px; }

    .msg-role-label {
      font-size: 11px; font-weight: 700; text-transform: uppercase;
      letter-spacing: 0.6px;
    }
    .role-user      .msg-role-label { color: var(--blue); }
    .role-assistant .msg-role-label { color: var(--green); }
    .role-system    .msg-role-label { color: var(--yellow); }

    .msg-bubble {
      padding: 14px 18px; border-radius: 8px;
      font-size: 14px; line-height: 1.75;
      white-space: pre-wrap; word-break: break-word;
    }
    .role-user      .msg-bubble { background: #0d1f36; border: 1px solid #1c3a5a; }
    .role-assistant .msg-bubble { background: #0a1f0a; border: 1px solid #1a3d1a; }
    .role-system    .msg-bubble {
      background: #1a1400; border: 1px solid #3d3000;
      font-size: 13px; color: #c9ab4a;
      max-height: 180px; overflow-y: auto;
    }

    /* collapsible messages */
    .msg.collapsed .msg-bubble { display: none; }
    .m-chevron {
      background: none; border: none; padding: 0; margin-right: 5px;
      color: var(--dim); font-size: 9px; cursor: pointer; font-family: inherit;
      display: inline-flex; align-items: center; flex-shrink: 0;
      transition: transform 0.15s; line-height: 1; vertical-align: middle;
    }
    .m-chevron:hover { color: var(--muted); background: none; }
    .msg.collapsed .m-chevron { transform: rotate(-90deg); }
    .msg-detail-btn {
      background: none; border: 1px solid var(--border);
      padding: 0 7px; border-radius: 4px; line-height: 18px;
      color: var(--dim); font-size: 11px; cursor: pointer; font-family: inherit;
    }
    .msg-detail-btn:hover { border-color: var(--yellow); color: var(--yellow); background: none; }

    /* ── Modal ── */
    #modal-overlay {
      display: none; position: fixed; inset: 0; z-index: 200;
      background: rgba(0,0,0,0.72);
      align-items: center; justify-content: center;
    }
    #modal-overlay.open { display: flex; }
    #modal-box {
      background: var(--bg2); border: 1px solid var(--border);
      border-radius: 10px;
      width: 740px; max-width: calc(100vw - 48px);
      max-height: calc(100vh - 80px);
      display: flex; flex-direction: column;
      box-shadow: 0 24px 56px rgba(0,0,0,0.6);
    }
    #modal-header {
      display: flex; align-items: center; justify-content: space-between;
      padding: 13px 20px; border-bottom: 1px solid var(--border); flex-shrink: 0;
    }
    #modal-title { font-size: 13px; font-weight: 700; color: var(--yellow); letter-spacing: 0.3px; }
    #modal-len { font-size: 11px; color: var(--dim); margin-left: 10px; }
    #modal-close {
      background: none; border: none; color: var(--muted);
      font-size: 18px; cursor: pointer; padding: 2px 6px; line-height: 1;
    }
    #modal-close:hover { color: var(--text); background: none; }
    #modal-body {
      padding: 22px 24px; overflow-y: auto; flex: 1;
      white-space: pre-wrap; word-break: break-word;
      font-size: 14px; line-height: 1.8; color: #c9ab4a;
    }

    /* ── Filter bar ── */
    #filter-bar {
      flex-shrink: 0;
      display: flex; align-items: center; gap: 8px;
      padding: 6px 16px;
      background: var(--bg2); border-bottom: 1px solid var(--border);
    }
    #filter-bar .filter-label {
      font-size: 11px; font-weight: 700; text-transform: uppercase;
      letter-spacing: 0.8px; color: var(--dim); margin-right: 4px;
    }
    .filter-btn {
      padding: 2px 10px; border-radius: 4px; font-size: 11px;
      border: 1px solid var(--border); background: var(--bg3);
      color: var(--dim); cursor: pointer; font-family: inherit;
      transition: all 0.1s; opacity: 0.5;
    }
    .filter-btn:hover { opacity: 0.8; }
    .filter-btn.active { opacity: 1; }
    .filter-btn.f-client-request.active  { border-color: var(--blue);   color: var(--blue); }
    .filter-btn.f-upstream-request.active { border-color: var(--muted);  color: var(--muted); }
    .filter-btn.f-upstream-response.active { border-color: var(--yellow); color: var(--yellow); }
    .filter-btn.f-client-response.active  { border-color: var(--green);  color: var(--green); }

    /* ── Request sections ── */
    .req-section { border-top: 1px solid var(--border2); }
    .req-section.collapsed .conv { display: none; }
    .section-label {
      padding: 5px 24px; font-size: 10px; font-weight: 700;
      text-transform: uppercase; letter-spacing: 0.8px;
      border-left: 3px solid transparent;
      cursor: pointer; user-select: none;
      display: flex; align-items: center;
    }
    .section-label:hover { filter: brightness(1.2); }
    .s-chevron {
      font-size: 8px; margin-right: 7px; flex-shrink: 0;
      transition: transform 0.15s; opacity: 0.8;
    }
    .req-section.collapsed .s-chevron { transform: rotate(-90deg); }
    .section-raw-btn {
      margin-left: auto; padding: 1px 8px; border-radius: 3px;
      border: 1px solid currentColor; background: transparent;
      font-size: 10px; cursor: pointer; font-family: inherit;
      opacity: 0.5; transition: opacity 0.1s; line-height: 16px; flex-shrink: 0;
    }
    .section-raw-btn:hover { opacity: 1; background: transparent; }
    .label-client-request   { color: var(--blue);   border-left-color: var(--blue);   background: #0d1520; }
    .label-upstream-request  { color: var(--muted);  border-left-color: var(--muted);  background: #13191f; }
    .label-upstream-response { color: var(--yellow); border-left-color: var(--yellow); background: #161200; }
    .label-client-response   { color: var(--green);  border-left-color: var(--green);  background: #0a1a0a; }

    /* ── Raw payload block ── */
    .raw-section { display: flex; flex-direction: column; gap: 4px; }
    .raw-label {
      font-size: 11px; font-weight: 700; text-transform: uppercase;
      letter-spacing: 0.6px; color: var(--dim);
    }
    .raw-block {
      padding: 10px 14px; border-radius: 6px;
      background: var(--bg); border: 1px solid var(--border2);
      font-size: 11px; line-height: 1.55; color: var(--muted);
      white-space: pre-wrap; word-break: break-all;
      max-height: 320px; overflow-y: auto; font-family: inherit;
    }

    /* ── Meta grid (for upstream-request / client-response) ── */
    .meta-grid { display: flex; flex-direction: column; gap: 4px; }
    .meta-row { display: flex; align-items: baseline; gap: 12px; font-size: 13px; }
    .meta-key { color: var(--dim); font-size: 11px; min-width: 110px; flex-shrink: 0; }
    .meta-val { color: var(--text); }

    /* ── Tools ── */
    .tools-section { display: flex; flex-direction: column; gap: 6px; }
    .tools-section.collapsed .tools-list { display: none; }
    .tools-label {
      font-size: 11px; font-weight: 700; text-transform: uppercase;
      letter-spacing: 0.6px; color: var(--purple);
      cursor: pointer; user-select: none;
      display: flex; align-items: center;
    }
    .tools-label:hover { opacity: 0.8; }
    .tools-section.collapsed .s-chevron { transform: rotate(-90deg); }
    .tools-list { display: flex; flex-direction: column; gap: 6px; }
    .tool-item {
      border: 1px solid #2d1f40; border-radius: 6px;
      background: #130d1e; overflow: hidden;
    }
    .tool-header {
      display: flex; align-items: center; gap: 8px;
      padding: 8px 14px; cursor: pointer;
      user-select: none;
    }
    .tool-header:hover { background: #1e1530; }
    .tool-badge {
      font-size: 10px; font-weight: 700; padding: 1px 6px;
      border-radius: 3px; background: #2a1545; color: var(--purple);
      flex-shrink: 0;
    }
    .tool-name { font-size: 13px; color: var(--text); flex-shrink: 0; }
    .tool-desc { font-size: 12px; color: var(--muted); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; flex: 1; }
    .tool-chevron { color: var(--dim); font-size: 10px; margin-left: auto; flex-shrink: 0; transition: transform 0.15s; }
    .tool-item.open .tool-chevron { transform: rotate(90deg); }
    .tool-params {
      display: none; padding: 10px 14px;
      border-top: 1px solid #2d1f40;
      font-size: 12px; line-height: 1.6;
      color: #c9b8e8; white-space: pre-wrap; word-break: break-all;
      max-height: 300px; overflow-y: auto;
    }
    .tool-item.open .tool-params { display: block; }

    /* ── Thinking ── */
    .thinking-section { display: flex; flex-direction: column; gap: 6px; }
    .thinking-label {
      font-size: 11px; font-weight: 700; text-transform: uppercase;
      letter-spacing: 0.6px; color: var(--yellow);
    }
    .thinking-bubble {
      padding: 12px 16px; border-radius: 8px;
      background: #1a1400; border: 1px solid #3d3000;
      font-size: 13px; line-height: 1.65;
      white-space: pre-wrap; word-break: break-word;
      color: #c9ab4a; max-height: 240px; overflow-y: auto;
    }

    /* ── Response ── */
    .resp-section { display: flex; flex-direction: column; gap: 6px; }
    .resp-label {
      font-size: 11px; font-weight: 700; text-transform: uppercase;
      letter-spacing: 0.6px; color: var(--green);
    }
    .resp-bubble {
      padding: 14px 18px; border-radius: 8px;
      background: #0a1f0a; border: 1px solid #1a3d1a;
      font-size: 14px; line-height: 1.75;
      white-space: pre-wrap; word-break: break-word;
    }

    /* ── Usage bar ── */
    .usage-bar {
      display: flex; gap: 16px; padding: 8px 24px;
      background: var(--bg); border-top: 1px solid var(--border2);
      font-size: 11px; color: var(--dim);
    }
    .usage-item strong { color: var(--muted); }

    .empty-conv {
      padding: 40px 32px; color: var(--muted); font-size: 13px;
    }
  </style>
</head>
<body>
  <div id="header">
    <h1>Proxy Viewer</h1>
    <span class="hdot">·</span>
    <span style="color:var(--muted);font-size:12px;" id="session-count">—</span>
    <button class="ml-auto" onclick="reload()">Refresh</button>
  </div>

  <div id="filter-bar">
    <span class="filter-label">Show</span>
    <button id="fbtn-clientRequest" class="filter-btn f-client-request" onclick="toggleFilter('clientRequest')">client-request</button>
    <button id="fbtn-upstreamRequest" class="filter-btn f-upstream-request" onclick="toggleFilter('upstreamRequest')">upstream-request</button>
    <button id="fbtn-upstreamResponse" class="filter-btn f-upstream-response" onclick="toggleFilter('upstreamResponse')">upstream-response</button>
    <button id="fbtn-clientResponse" class="filter-btn f-client-response" onclick="toggleFilter('clientResponse')">client-response</button>
  </div>

  <div id="body">
    <div id="sidebar">
      <div id="sidebar-header">
        <span class="sidebar-title">Sessions &nbsp;<span id="session-count-sidebar"></span></span>
        <button id="sidebar-toggle" onclick="toggleSidebar()" title="Toggle sidebar">
          <svg width="14" height="14" viewBox="0 0 14 14" fill="none" xmlns="http://www.w3.org/2000/svg">
            <path d="M9 2L4 7L9 12" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round"/>
          </svg>
        </button>
      </div>
      <div id="session-list">
        <div style="padding:20px 14px;color:var(--muted);">Loading&hellip;</div>
      </div>
    </div>

    <div id="main">
      <div id="session-bar" style="display:none;">
        <span id="session-bar-id"></span>
        <span id="session-bar-meta"></span>
        <div class="session-bar-dot">
          <button class="session-bar-dot-btn" onclick="toggleSessionMenu(event)" title="Options">&#x22EE;</button>
          <div id="session-bar-menu" class="session-bar-menu">
            <div class="session-bar-menu-item danger" onclick="deleteActiveSession()">
              <svg width="13" height="13" viewBox="0 0 13 13" fill="none"><path d="M2 3.5h9M4.5 3.5V2.5a.5.5 0 0 1 .5-.5h3a.5.5 0 0 1 .5.5v1M5 6v4M8 6v4M3 3.5l.5 7a.5.5 0 0 0 .5.5h5a.5.5 0 0 0 .5-.5l.5-7" stroke="currentColor" stroke-width="1.2" stroke-linecap="round" stroke-linejoin="round"/></svg>
              Delete session
            </div>
          </div>
        </div>
      </div>
      <div id="conv">
        <div id="main-empty">
          <div class="icon">&#x1F4AC;</div>
          <p>Select a session to view the conversation</p>
        </div>
      </div>
    </div>
  </div>

  <div id="modal-overlay" onclick="closeModal(event)">
    <div id="modal-box">
      <div id="modal-header">
        <div>
          <span id="modal-title">System Prompt</span>
          <span id="modal-len"></span>
        </div>
        <button id="modal-close" onclick="closeModal()">&#x2715;</button>
      </div>
      <div id="modal-body"></div>
    </div>
  </div>

  <script>
    var activeSessionId = null;
    var systemPromptContents = {};
    var rawPayloads = {};

    /* ── Filter ── */
    var FILTER_KEY = 'proxy_viewer_filter_v1';
    var filter = { clientRequest: true, upstreamRequest: true, upstreamResponse: true, clientResponse: true };

    function loadFilter() {
      try {
        var saved = localStorage.getItem(FILTER_KEY);
        if (saved) {
          var parsed = JSON.parse(saved);
          Object.keys(filter).forEach(function(k) { if (k in parsed) filter[k] = parsed[k]; });
        }
      } catch(e) {}
      updateFilterButtons();
    }

    function saveFilter() {
      try { localStorage.setItem(FILTER_KEY, JSON.stringify(filter)); } catch(e) {}
    }

    function toggleFilter(key) {
      filter[key] = !filter[key];
      saveFilter();
      updateFilterButtons();
      var classMap = {
        clientRequest: 'section-client-request',
        upstreamRequest: 'section-upstream-request',
        upstreamResponse: 'section-upstream-response',
        clientResponse: 'section-client-response',
      };
      var els = document.querySelectorAll('.' + classMap[key]);
      for (var i = 0; i < els.length; i++) {
        els[i].style.display = filter[key] ? '' : 'none';
      }
    }

    function updateFilterButtons() {
      var keys = ['clientRequest', 'upstreamRequest', 'upstreamResponse', 'clientResponse'];
      for (var i = 0; i < keys.length; i++) {
        var btn = document.getElementById('fbtn-' + keys[i]);
        if (btn) btn.classList.toggle('active', filter[keys[i]]);
      }
    }

    /* ── Helpers ── */

    function esc(s) {
      return String(s || '').replace(/&/g,'&amp;').replace(/</g,'&lt;').replace(/>/g,'&gt;').replace(/"/g,'&quot;');
    }

    function ago(ms) {
      var diff = Date.now() - ms;
      if (diff < 60000)   return Math.floor(diff / 1000) + 's ago';
      if (diff < 3600000) return Math.floor(diff / 60000) + 'm ago';
      if (diff < 86400000) return Math.floor(diff / 3600000) + 'h ago';
      return Math.floor(diff / 86400000) + 'd ago';
    }

    function dur(ms) {
      if (!ms || ms <= 0) return '-';
      if (ms < 1000) return ms + 'ms';
      return (ms / 1000).toFixed(1) + 's';
    }

    function fmt(n) {
      return n ? n.toLocaleString() : '0';
    }

    function hhmm(ms) {
      var d = new Date(ms);
      var h = String(d.getHours()).padStart(2,'0');
      var m = String(d.getMinutes()).padStart(2,'0');
      var s = String(d.getSeconds()).padStart(2,'0');
      return h + ':' + m + ':' + s;
    }

    function providerClass(p) {
      var map = { openai:'p-openai', gemini:'p-gemini', anthropic:'p-anthropic', copilot:'p-copilot' };
      return map[(p||'').toLowerCase()] || 'p-default';
    }

    function statusClass(code) {
      if (!code)            return 'status-pending';
      if (code >= 200 && code < 300) return 'status-ok';
      return 'status-err';
    }

    /* ── Sessions ── */

    function loadSessions() {
      fetch('/viewer/api/sessions?limit=200')
        .then(function(r) { return r.json(); })
        .then(function(data) { renderSessions(data || []); })
        .catch(function(e) { console.error(e); });
    }

    function renderSessions(sessions) {
      var countText = sessions.length + ' session' + (sessions.length === 1 ? '' : 's');
      document.getElementById('session-count').textContent = countText;
      document.getElementById('session-count-sidebar').textContent = sessions.length;

      var list = document.getElementById('session-list');
      if (!sessions.length) {
        list.innerHTML = '<div style="padding:20px 14px;color:var(--muted);">No sessions yet</div>';
        return;
      }

      var html = '';
      for (var i = 0; i < sessions.length; i++) {
        var s = sessions[i];
        var isActive = s.sessionId === activeSessionId;
        var shortId = s.sessionId.length > 20 ? s.sessionId.slice(0, 20) + '\u2026' : s.sessionId;
        var providers = (s.providers || '').split(',');
        var providerBadges = '';
        for (var j = 0; j < providers.length; j++) {
          var p = providers[j].trim();
          if (p) providerBadges += '<span class="session-provider ' + providerClass(p) + '">' + esc(p) + '</span>';
        }
        html += '<div class="session-item' + (isActive ? ' active' : '') + '"';
        html += ' onclick="selectSession(\'' + esc(s.sessionId) + '\')"';
        html += ' title="' + esc(s.sessionId) + '">';
        html += '<div class="session-id">' + esc(shortId) + '</div>';
        html += '<div class="session-meta">';
        html += providerBadges;
        html += '<span>' + s.requestCount + ' req' + (s.requestCount === 1 ? '' : 's') + '</span>';
        if (s.totalTokens) html += '<span>' + fmt(s.totalTokens) + ' tok</span>';
        html += '<span>' + ago(s.lastActivity) + '</span>';
        html += '</div>';
        html += '</div>';
      }
      list.innerHTML = html;
    }

    /* ── Session detail ── */

    function selectSession(sessionId) {
      activeSessionId = sessionId;
      renderSessions_highlight();
      loadSessionDetail(sessionId);
    }

    function toggleSidebar() {
      document.getElementById('sidebar').classList.toggle('collapsed');
    }

    function toggleSessionMenu(evt) {
      evt.stopPropagation();
      document.getElementById('session-bar-menu').classList.toggle('open');
    }

    document.addEventListener('click', function() {
      var menu = document.getElementById('session-bar-menu');
      if (menu) menu.classList.remove('open');
    });

    function deleteActiveSession() {
      document.getElementById('session-bar-menu').classList.remove('open');
      if (!activeSessionId) return;
      var sid = activeSessionId;
      if (!confirm('Delete session "' + sid + '"?\nThis cannot be undone.')) return;
      fetch('/viewer/api/sessions/' + encodeURIComponent(sid), { method: 'DELETE' })
        .then(function(r) {
          if (!r.ok) throw new Error('Delete failed: ' + r.status);
          activeSessionId = null;
          document.getElementById('session-bar').style.display = 'none';
          document.getElementById('conv').innerHTML =
            '<div style="padding:40px 32px;color:var(--muted);">Select a session to view details</div>';
          loadSessions();
        })
        .catch(function(e) { alert(e.message); });
    }

    function renderSessions_highlight() {
      var items = document.querySelectorAll('.session-item');
      for (var i = 0; i < items.length; i++) {
        var el = items[i];
        var onclick = el.getAttribute('onclick') || '';
        var isActive = onclick.indexOf("'" + activeSessionId + "'") !== -1;
        el.classList.toggle('active', isActive);
      }
    }

    function loadSessionDetail(sessionId) {
      var conv = document.getElementById('conv');
      conv.innerHTML = '<div style="padding:40px 32px;color:var(--muted);">Loading&hellip;</div>';

      fetch('/viewer/api/sessions/' + encodeURIComponent(sessionId))
        .then(function(r) { return r.json(); })
        .then(function(data) { renderConversation(data || []); })
        .catch(function() {
          document.getElementById('conv').innerHTML =
            '<div style="padding:40px 32px;color:var(--red);">Failed to load session</div>';
        });
    }

    function renderConversation(requests) {
      var conv = document.getElementById('conv');
      var bar = document.getElementById('session-bar');
      var totalMsgs = 0;
      for (var ii = 0; ii < requests.length; ii++) {
        var msgs = requests[ii].messages || [];
        for (var jj = 0; jj < msgs.length; jj++) {
          if (msgs[jj].role === 'user') totalMsgs++;
        }
      }
      document.getElementById('session-bar-id').textContent = activeSessionId || '';
      document.getElementById('session-bar-meta').textContent =
        requests.length + ' request' + (requests.length === 1 ? '' : 's') +
        ' · ' + totalMsgs + ' message' + (totalMsgs === 1 ? '' : 's');
      bar.style.display = 'flex';

      if (!requests.length) {
        conv.innerHTML = '<div class="empty-conv">No requests in this session</div>';
        return;
      }

      var html = '';
      for (var i = 0; i < requests.length; i++) {
        var d = requests[i];
        var r = d.request;
        var sc = r.statusCode;

        html += '<div class="req-block">';

        /* pre-compute pretty raw payloads */
        var prettyRawBody = d.rawBody || '';
        try { if (prettyRawBody) prettyRawBody = JSON.stringify(JSON.parse(prettyRawBody), null, 2); } catch(er) {}
        var prettyRawResp = d.rawResponse || '';
        try { if (prettyRawResp) prettyRawResp = JSON.stringify(JSON.parse(prettyRawResp), null, 2); } catch(er) {}
        var prettyRawClientResp = d.rawClientResponse || '';
        try { if (prettyRawClientResp) prettyRawClientResp = JSON.stringify(JSON.parse(prettyRawClientResp), null, 2); } catch(er) {}
        rawPayloads['cr-' + i]  = prettyRawBody;
        rawPayloads['ur-' + i]  = prettyRawBody;
        rawPayloads['us-' + i]  = prettyRawResp;
        rawPayloads['cre-' + i] = prettyRawClientResp;

        /* ── sticky req header ── */
        html += '<div class="req-header">';
        html += '<span class="req-provider ' + providerClass(r.provider) + '">' + esc(r.provider) + '</span>';
        html += '<span class="req-model">' + esc(r.model || '-') + '</span>';
        html += '<span class="req-sep">·</span>';
        html += '<span class="' + statusClass(sc) + '">' + (sc || '&hellip;') + '</span>';
        if (r.totalTokens) {
          html += '<span class="req-sep">·</span>';
          html += '<span class="req-tokens">' + fmt(r.totalTokens) + ' tok</span>';
        }
        html += '<span class="req-sep">·</span>';
        html += '<span>' + dur(r.durationMs) + '</span>';
        html += '<span class="req-time">' + hhmm(r.createdAt) + '</span>';
        html += '</div>';

        /* ── client-request ── */
        html += '<div class="req-section section-client-request"' + (filter.clientRequest ? '' : ' style="display:none"') + '>';
        html += '<div class="section-label label-client-request" onclick="toggleSection(this.parentElement)"><span class="s-chevron">&#9660;</span>client-request';
        if (d.rawBody) html += '<button class="section-raw-btn" onclick="event.stopPropagation();openRawModal(\'cr-'+i+'\')">raw</button>';
        html += '</div>';
        html += '<div class="conv">';

        var msgs = d.messages || [];
        var systemMsgs = [], convMsgs = [];
        for (var j = 0; j < msgs.length; j++) {
          if (msgs[j].role === 'system') systemMsgs.push(msgs[j]);
          else convMsgs.push(msgs[j]);
        }
        for (var j = 0; j < systemMsgs.length; j++) {
          var m = systemMsgs[j];
          var uid = 'sm-' + i + '-' + j;
          systemPromptContents[uid] = m.content;
          html += '<div class="msg role-system collapsed" id="' + uid + '">';
          html += '<div class="msg-role-label">';
          html += '<button class="m-chevron" onclick="toggleMsg(\'' + uid + '\')">&#9660;</button>';
          html += 'System';
          html += '<button class="msg-detail-btn" onclick="openSystemModal(\'' + uid + '\')">detail</button>';
          html += '</div>';
          html += '<div class="msg-bubble">' + esc(m.content) + '</div>';
          html += '</div>';
        }
        for (var j = 0; j < convMsgs.length; j++) {
          var m = convMsgs[j];
          var mid = 'cm-' + i + '-' + j;
          html += '<div class="msg role-' + esc(m.role || 'user') + '" id="' + mid + '">';
          html += '<div class="msg-role-label">';
          html += '<button class="m-chevron" onclick="toggleMsg(\'' + mid + '\')">&#9660;</button>';
          html += esc(m.role || 'user');
          if (m.name) html += ' <span style="color:var(--dim);font-weight:400;">' + esc(m.name) + '</span>';
          html += '</div>';
          html += '<div class="msg-bubble">' + esc(m.content) + '</div>';
          html += '</div>';
        }
        var tools = d.tools || [];
        if (tools.length) {
          html += '<div class="tools-section">';
          html += '<div class="tools-label" onclick="toggleSection(this.parentElement)"><span class="s-chevron">&#9660;</span>Tools (' + tools.length + ')</div>';
          html += '<div class="tools-list">';
          for (var j = 0; j < tools.length; j++) {
            var t = tools[j];
            var tid = 'tool-' + i + '-' + j;
            var prettyParams = t.parameters;
            try { prettyParams = JSON.stringify(JSON.parse(t.parameters), null, 2); } catch(e2) {}
            html += '<div class="tool-item" id="' + tid + '">';
            html += '<div class="tool-header" onclick="toggleTool(\'' + tid + '\')">';
            html += '<span class="tool-badge">' + esc(t.type || 'fn') + '</span>';
            html += '<span class="tool-name">' + esc(t.name) + '</span>';
            if (t.description) html += '<span class="tool-desc">' + esc(t.description) + '</span>';
            html += '<span class="tool-chevron">&#9654;</span>';
            html += '</div>';
            html += '<div class="tool-params">' + esc(prettyParams) + '</div>';
            html += '</div>';
          }
          html += '</div></div>';
        }
        html += '</div></div>'; /* .conv + .req-section */

        /* ── upstream-request ── */
        html += '<div class="req-section section-upstream-request"' + (filter.upstreamRequest ? '' : ' style="display:none"') + '>';
        html += '<div class="section-label label-upstream-request" onclick="toggleSection(this.parentElement)"><span class="s-chevron">&#9660;</span>upstream-request';
        if (d.rawBody) html += '<button class="section-raw-btn" onclick="event.stopPropagation();openRawModal(\'ur-'+i+'\')">raw</button>';
        html += '</div>';
        html += '<div class="conv">';
        for (var j = 0; j < systemMsgs.length; j++) {
          var m = systemMsgs[j];
          var uid2 = 'usm-' + i + '-' + j;
          systemPromptContents[uid2] = m.content;
          html += '<div class="msg role-system collapsed" id="' + uid2 + '">';
          html += '<div class="msg-role-label">';
          html += '<button class="m-chevron" onclick="toggleMsg(\'' + uid2 + '\')">&#9660;</button>';
          html += 'System';
          html += '<button class="msg-detail-btn" onclick="openSystemModal(\'' + uid2 + '\')">detail</button>';
          html += '</div>';
          html += '<div class="msg-bubble">' + esc(m.content) + '</div>';
          html += '</div>';
        }
        for (var j = 0; j < convMsgs.length; j++) {
          var m = convMsgs[j];
          var mid2 = 'ucm-' + i + '-' + j;
          html += '<div class="msg role-' + esc(m.role || 'user') + '" id="' + mid2 + '">';
          html += '<div class="msg-role-label">';
          html += '<button class="m-chevron" onclick="toggleMsg(\'' + mid2 + '\')">&#9660;</button>';
          html += esc(m.role || 'user');
          if (m.name) html += ' <span style="color:var(--dim);font-weight:400;">' + esc(m.name) + '</span>';
          html += '</div>';
          html += '<div class="msg-bubble">' + esc(m.content) + '</div>';
          html += '</div>';
        }
        if (tools.length) {
          html += '<div class="tools-section">';
          html += '<div class="tools-label" onclick="toggleSection(this.parentElement)"><span class="s-chevron">&#9660;</span>Tools (' + tools.length + ')</div>';
          html += '<div class="tools-list">';
          for (var j = 0; j < tools.length; j++) {
            var t2 = tools[j];
            var tid2 = 'utool-' + i + '-' + j;
            var pp2 = t2.parameters;
            try { pp2 = JSON.stringify(JSON.parse(t2.parameters), null, 2); } catch(e5) {}
            html += '<div class="tool-item" id="' + tid2 + '">';
            html += '<div class="tool-header" onclick="toggleTool(\'' + tid2 + '\')">';
            html += '<span class="tool-badge">' + esc(t2.type || 'fn') + '</span>';
            html += '<span class="tool-name">' + esc(t2.name) + '</span>';
            if (t2.description) html += '<span class="tool-desc">' + esc(t2.description) + '</span>';
            html += '<span class="tool-chevron">&#9654;</span>';
            html += '</div>';
            html += '<div class="tool-params">' + esc(pp2) + '</div>';
            html += '</div>';
          }
          html += '</div></div>';
        }
        html += '</div></div>'; /* .conv + .req-section */

        /* ── upstream-response ── */
        html += '<div class="req-section section-upstream-response"' + (filter.upstreamResponse ? '' : ' style="display:none"') + '>';
        html += '<div class="section-label label-upstream-response" onclick="toggleSection(this.parentElement)"><span class="s-chevron">&#9660;</span>upstream-response';
        if (d.rawResponse) html += '<button class="section-raw-btn" onclick="event.stopPropagation();openRawModal(\'us-'+i+'\')">raw</button>';
        html += '</div>';
        html += '<div class="conv">';
        if (d.thinking) {
          html += '<div class="thinking-section">';
          html += '<div class="thinking-label">Thinking</div>';
          html += '<div class="thinking-bubble">' + esc(d.thinking) + '</div>';
          html += '</div>';
        }
        if (d.response) {
          html += '<div class="resp-section">';
          html += '<div class="resp-label">Response</div>';
          html += '<div class="resp-bubble">' + esc(d.response) + '</div>';
          html += '</div>';
        }
        if (!d.thinking && !d.response) {
          if (prettyRawResp) {
            html += '<div class="resp-section">';
            html += '<div class="resp-label">Response (raw)</div>';
            html += '<div class="resp-bubble">' + esc(prettyRawResp) + '</div>';
            html += '</div>';
          } else {
            html += '<span style="color:var(--dim);font-size:12px;">No response captured</span>';
          }
        }
        html += '</div></div>';

        /* ── client-response ── */
        html += '<div class="req-section section-client-response"' + (filter.clientResponse ? '' : ' style="display:none"') + '>';
        html += '<div class="section-label label-client-response" onclick="toggleSection(this.parentElement)"><span class="s-chevron">&#9660;</span>client-response';
        if (d.rawClientResponse) html += '<button class="section-raw-btn" onclick="event.stopPropagation();openRawModal(\'cre-'+i+'\')">raw</button>';
        html += '</div>';
        html += '<div class="conv"><div class="meta-grid">';
        html += '<div class="meta-row"><span class="meta-key">status</span><span class="meta-val ' + statusClass(sc) + '">' + (sc || '-') + '</span></div>';
        html += '<div class="meta-row"><span class="meta-key">duration</span><span class="meta-val">' + dur(r.durationMs) + '</span></div>';
        if (r.totalTokens) {
          html += '<div class="meta-row"><span class="meta-key">prompt tokens</span><span class="meta-val">' + fmt(r.promptTokens) + '</span></div>';
          html += '<div class="meta-row"><span class="meta-key">completion tokens</span><span class="meta-val">' + fmt(r.completionTokens) + '</span></div>';
          html += '<div class="meta-row"><span class="meta-key">total tokens</span><span class="meta-val">' + fmt(r.totalTokens) + '</span></div>';
          if (r.cacheReadTokens) html += '<div class="meta-row"><span class="meta-key">cache read</span><span class="meta-val">' + fmt(r.cacheReadTokens) + '</span></div>';
        }
        html += '</div>';
        /* response content from raw_client_response */
        if (prettyRawClientResp) {
          html += '<div class="resp-bubble">' + esc(prettyRawClientResp) + '</div>';
        } else {
          html += '<div style="padding:8px 0;color:var(--dim);font-size:12px;">No client response captured</div>';
        }
        html += '</div>';

        html += '</div>'; /* .req-block */
      }

      conv.innerHTML = html;
    }

    function toggleMsg(id) {
      var el = document.getElementById(id);
      if (el) el.classList.toggle('collapsed');
    }

    function toggleTool(id) {
      var el = document.getElementById(id);
      if (el) el.classList.toggle('open');
    }

    function toggleSection(el) {
      if (el) el.classList.toggle('collapsed');
    }

    function openSystemModal(uid) {
      var content = systemPromptContents[uid] || '';
      document.getElementById('modal-title').textContent = 'System Prompt';
      document.getElementById('modal-body').textContent = content;
      document.getElementById('modal-len').textContent = content.length.toLocaleString() + ' chars';
      document.getElementById('modal-overlay').classList.add('open');
    }

    function openRawModal(uid) {
      var content = rawPayloads[uid] || '';
      document.getElementById('modal-title').textContent = 'Raw Payload';
      document.getElementById('modal-body').textContent = content;
      document.getElementById('modal-len').textContent = content.length.toLocaleString() + ' chars';
      document.getElementById('modal-overlay').classList.add('open');
    }

    function closeModal(e) {
      if (e && e.target !== document.getElementById('modal-overlay')) return;
      document.getElementById('modal-overlay').classList.remove('open');
    }

    document.addEventListener('keydown', function(e) {
      if (e.key === 'Escape') document.getElementById('modal-overlay').classList.remove('open');
    });

    /* ── Auto-refresh ── */

    function reload() {
      loadSessions();
      if (activeSessionId) loadSessionDetail(activeSessionId);
    }

    loadFilter();
    loadSessions();
  </script>
</body>
</html>`
