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
	ID               string `json:"id"`
	SessionID        string `json:"sessionId"`
	Provider         string `json:"provider"`
	Model            string `json:"model"`
	Stream           bool   `json:"stream"`
	StatusCode       int    `json:"statusCode"`
	CreatedAt        int64  `json:"createdAt"`
	CompletedAt      *int64 `json:"completedAt"`
	DurationMs       int64  `json:"durationMs"`
	PromptTokens     int    `json:"promptTokens"`
	CompletionTokens int    `json:"completionTokens"`
	TotalTokens      int    `json:"totalTokens"`
	CacheReadTokens  int    `json:"cacheReadTokens"`
}

type MessageView struct {
	Role    string `json:"role"`
	Content string `json:"content"`
	Name    string `json:"name"`
}

type RequestDetailView struct {
	Request  RequestViewRow `json:"request"`
	Messages []MessageView  `json:"messages"`
	Response string         `json:"response"`
	Thinking string         `json:"thinking"`
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
			r.id, r.session_id, r.provider, COALESCE(r.model, ''), r.stream,
			COALESCE(r.status_code, 0), r.created_at, r.completed_at,
			COALESCE(u.prompt_tokens, 0), COALESCE(u.completion_tokens, 0),
			COALESCE(u.total_tokens, 0), COALESCE(u.cache_read_tokens, 0)
		FROM requests r
		LEFT JOIN usage u ON u.request_id = r.id
		WHERE r.session_id = ?
		ORDER BY r.created_at ASC
	`, sessionID)
	if err != nil {
		return nil, fmt.Errorf("list session requests: %w", err)
	}
	defer rows.Close()

	var requestRows []RequestViewRow
	for rows.Next() {
		var row RequestViewRow
		var stream int
		var completedAt sql.NullInt64
		if err := rows.Scan(
			&row.ID, &row.SessionID, &row.Provider, &row.Model, &stream,
			&row.StatusCode, &row.CreatedAt, &completedAt,
			&row.PromptTokens, &row.CompletionTokens, &row.TotalTokens, &row.CacheReadTokens,
		); err != nil {
			return nil, fmt.Errorf("scan session request: %w", err)
		}
		row.Stream = stream == 1
		if completedAt.Valid {
			v := completedAt.Int64
			row.CompletedAt = &v
			row.DurationMs = v - row.CreatedAt
		}
		requestRows = append(requestRows, row)
	}
	rows.Close()

	var details []RequestDetailView
	for _, req := range requestRows {
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

		details = append(details, RequestDetailView{
			Request:  req,
			Messages: messages,
			Response: sb.String(),
			Thinking: thinkingSb.String(),
		})
	}
	return details, nil
}

func (db *Database) ListRequestViews(limit int) ([]RequestViewRow, error) {
	rows, err := db.conn.Query(`
		SELECT
			r.id, r.session_id, r.provider, COALESCE(r.model, ''), r.stream,
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
		if err := rows.Scan(
			&row.ID, &row.SessionID, &row.Provider, &row.Model, &stream,
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
		result = append(result, row)
	}
	return result, nil
}

func (db *Database) GetRequestViewDetail(id string) (*RequestDetailView, error) {
	var row RequestViewRow
	var stream int
	var completedAt sql.NullInt64

	err := db.conn.QueryRow(`
		SELECT
			r.id, r.session_id, r.provider, COALESCE(r.model, ''), r.stream,
			COALESCE(r.status_code, 0), r.created_at, r.completed_at,
			COALESCE(u.prompt_tokens, 0), COALESCE(u.completion_tokens, 0),
			COALESCE(u.total_tokens, 0), COALESCE(u.cache_read_tokens, 0)
		FROM requests r
		LEFT JOIN usage u ON u.request_id = r.id
		WHERE r.id = ?
	`, id).Scan(
		&row.ID, &row.SessionID, &row.Provider, &row.Model, &stream,
		&row.StatusCode, &row.CreatedAt, &completedAt,
		&row.PromptTokens, &row.CompletionTokens, &row.TotalTokens, &row.CacheReadTokens,
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

	return &RequestDetailView{
		Request:  row,
		Messages: messages,
		Response: sb.String(),
		Thinking: thinkingSb.String(),
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
    }

    #sidebar-header {
      padding: 10px 14px;
      background: var(--bg2); border-bottom: 1px solid var(--border);
      font-size: 11px; font-weight: 700; text-transform: uppercase;
      letter-spacing: 0.8px; color: var(--muted);
      flex-shrink: 0;
    }

    #session-list { flex: 1; overflow-y: auto; }

    .session-item {
      padding: 11px 14px; border-bottom: 1px solid var(--border2);
      cursor: pointer; border-left: 3px solid transparent;
      transition: background 0.1s;
    }
    .session-item:hover { background: var(--bg3); }
    .session-item.active {
      background: #1c2b3a; border-left-color: var(--blue);
    }
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

    /* collapsible system message */
    .msg-toggle {
      background: none; border: none; padding: 0 4px;
      color: var(--dim); font-size: 11px; cursor: pointer;
      display: inline-flex; align-items: center; gap: 4px;
    }
    .msg-toggle:hover { color: var(--muted); background: none; }
    .msg.collapsed .msg-bubble { display: none; }
    .msg.collapsed .msg-toggle::before { content: "show"; }
    .msg:not(.collapsed) .msg-toggle::before { content: "hide"; }
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

  <div id="body">
    <div id="sidebar">
      <div id="sidebar-header">Sessions</div>
      <div id="session-list">
        <div style="padding:20px 14px;color:var(--muted);">Loading&hellip;</div>
      </div>
    </div>

    <div id="main">
      <div id="main-empty">
        <div class="icon">&#x1F4AC;</div>
        <p>Select a session to view the conversation</p>
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
      var el = document.getElementById('session-count');
      el.textContent = sessions.length + ' session' + (sessions.length === 1 ? '' : 's');

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
        html += '</div></div>';
      }
      list.innerHTML = html;
    }

    /* ── Session detail ── */

    function selectSession(sessionId) {
      activeSessionId = sessionId;
      renderSessions_highlight();
      loadSessionDetail(sessionId);
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
      var main = document.getElementById('main');
      main.innerHTML = '<div style="padding:40px 32px;color:var(--muted);">Loading&hellip;</div>';

      fetch('/viewer/api/sessions/' + encodeURIComponent(sessionId))
        .then(function(r) { return r.json(); })
        .then(function(data) { renderConversation(data || []); })
        .catch(function() {
          document.getElementById('main').innerHTML =
            '<div style="padding:40px 32px;color:var(--red);">Failed to load session</div>';
        });
    }

    function renderConversation(requests) {
      var main = document.getElementById('main');
      if (!requests.length) {
        main.innerHTML = '<div class="empty-conv">No requests in this session</div>';
        return;
      }

      var html = '';
      for (var i = 0; i < requests.length; i++) {
        var d = requests[i];
        var r = d.request;
        var sc = r.statusCode;

        /* request header */
        html += '<div class="req-block">';
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

        /* conversation */
        html += '<div class="conv">';

        var msgs = d.messages || [];
        var systemMsgs = [];
        var convMsgs = [];
        for (var j = 0; j < msgs.length; j++) {
          if (msgs[j].role === 'system') systemMsgs.push(msgs[j]);
          else convMsgs.push(msgs[j]);
        }

        /* system messages collapsed */
        for (var j = 0; j < systemMsgs.length; j++) {
          var m = systemMsgs[j];
          var uid = 'sm-' + i + '-' + j;
          systemPromptContents[uid] = m.content;
          html += '<div class="msg role-system collapsed" id="' + uid + '">';
          html += '<div class="msg-role-label">System ';
          html += '<button class="msg-toggle" onclick="toggleMsg(\'' + uid + '\')"></button>';
          html += '<button class="msg-detail-btn" onclick="openSystemModal(\'' + uid + '\')">detail</button>';
          html += '</div>';
          html += '<div class="msg-bubble">' + esc(m.content) + '</div>';
          html += '</div>';
        }

        /* user/assistant messages */
        for (var j = 0; j < convMsgs.length; j++) {
          var m = convMsgs[j];
          var roleClass = 'role-' + (m.role || 'user');
          html += '<div class="msg ' + roleClass + '">';
          html += '<div class="msg-role-label">' + esc(m.role || 'user');
          if (m.name) html += ' <span style="color:var(--dim);font-weight:400;">' + esc(m.name) + '</span>';
          html += '</div>';
          html += '<div class="msg-bubble">' + esc(m.content) + '</div>';
          html += '</div>';
        }

        /* thinking */
        if (d.thinking) {
          html += '<div class="thinking-section">';
          html += '<div class="thinking-label">Thinking</div>';
          html += '<div class="thinking-bubble">' + esc(d.thinking) + '</div>';
          html += '</div>';
        }

        /* response */
        if (d.response) {
          html += '<div class="resp-section">';
          html += '<div class="resp-label">Response</div>';
          html += '<div class="resp-bubble">' + esc(d.response) + '</div>';
          html += '</div>';
        }

        html += '</div>'; /* .conv */

        /* usage bar */
        if (r.totalTokens) {
          html += '<div class="usage-bar">';
          html += '<span><strong>Prompt</strong> ' + fmt(r.promptTokens) + '</span>';
          html += '<span><strong>Completion</strong> ' + fmt(r.completionTokens) + '</span>';
          html += '<span><strong>Total</strong> ' + fmt(r.totalTokens) + '</span>';
          if (r.cacheReadTokens) html += '<span><strong>Cache read</strong> ' + fmt(r.cacheReadTokens) + '</span>';
          html += '</div>';
        }

        html += '</div>'; /* .req-block */
      }

      main.innerHTML = html;
    }

    function toggleMsg(id) {
      var el = document.getElementById(id);
      if (el) el.classList.toggle('collapsed');
    }

    function openSystemModal(uid) {
      var content = systemPromptContents[uid] || '';
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

    loadSessions();
  </script>
</body>
</html>`
