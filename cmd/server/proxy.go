package main

import (
	"bytes"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

type ProxyServer struct {
	cfg          Config
	db           *Database
	client       *http.Client
	log          *slog.Logger
	queue        chan queueItem
	inflight     map[string]struct{}
	sessionIndex map[string]string
	mu           sync.Mutex
}

type queueItem struct {
	start chan struct{}
	done  chan struct{}
}

func NewProxyServer(cfg Config, db *Database, log *slog.Logger) *ProxyServer {
	server := &ProxyServer{
		cfg: cfg,
		db:  db,
		client: &http.Client{
			Timeout: cfg.RequestTimeout,
		},
		log:          log,
		queue:        make(chan queueItem),
		inflight:     make(map[string]struct{}),
		sessionIndex: make(map[string]string),
	}
	go server.runQueue()
	return server
}

func (s *ProxyServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/healthz":
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
		return
	case r.Method == http.MethodGet && r.URL.Path == "/config":
		s.handleConfigPage(w)
		return
	case r.Method == http.MethodGet && r.URL.Path == "/test":
		s.handleTestPage(w)
		return
	case r.Method == http.MethodGet && r.URL.Path == "/test/mock/gemini":
		s.handleGeminiMock(w)
		return
	}

	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	s.handleProxy(w, r)
}

func (s *ProxyServer) runQueue() {
	for item := range s.queue {
		close(item.start)
		<-item.done
		if s.cfg.RateLimitInterval > 0 {
			time.Sleep(s.cfg.RateLimitInterval)
		}
	}
}

func (s *ProxyServer) handleProxy(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "failed to read body", http.StatusBadRequest)
		return
	}

	target, ok := s.resolveTarget(r.URL.Path)
	if !ok {
		http.Error(w, "unsupported path", http.StatusNotFound)
		return
	}
	parsed := s.parseRequest(body, r.Header, target.format)
	if parsed.SessionID == "" {
		parsed.SessionID = s.getSessionID(parsed.UserTurns)
	}

	fingerprint := buildFingerprint(parsed.SessionID, r.URL.Path, body)
	if !s.acquireFingerprint(fingerprint) {
		s.log.Info("dropped retry", "session", parsed.SessionID, "path", r.URL.Path)
		w.WriteHeader(http.StatusNoContent)
		return
	}
	defer s.releaseFingerprint(fingerprint)

	item := queueItem{start: make(chan struct{}), done: make(chan struct{})}
	s.queue <- item
	<-item.start
	defer close(item.done)

	requestID := uuid.New().String()
	createdAt := time.Now()
	startedAt := time.Now()

	s.log.Info("client request", "id", requestID, "session", parsed.SessionID, "provider", target.provider, "method", r.Method, "path", r.URL.Path, "model", parsed.Model, "messages", len(parsed.Messages), "bytes", len(body))
	if err := s.appendLogLine(parsed.SessionID, proxyLogEntry{
		Timestamp: startedAt,
		Event:     "client_request",
		RequestID: requestID,
		Provider:  target.provider,
		Method:    r.Method,
		Path:      r.URL.Path,
		Model:     parsed.Model,
		Messages:  len(parsed.Messages),
		Bytes:     len(body),
	}); err != nil {
		s.log.Error("write proxy log failed", "error", err)
	}

	var dbg debugRecord
	dbg.ClientRequest = &debugPayload{
		Timestamp: startedAt,
		RequestID: requestID,
		SessionID: parsed.SessionID,
		Provider:  target.provider,
		Format:    target.format,
		Path:      r.URL.Path,
		Body:      decodeDebugBody(body),
		Headers:   sanitizeHeaders(r.Header),
	}

	record := RequestRecord{
		ID:          requestID,
		SessionID:   parsed.SessionID,
		Provider:    target.provider,
		Model:       parsed.Model,
		Stream:      parsed.Stream,
		Path:        r.URL.Path,
		User:        parsed.User,
		Temperature: parsed.Temperature,
		TopP:        parsed.TopP,
		MaxTokens:   parsed.MaxTokens,
		CreatedAt:   createdAt,
	}

	if err := s.db.InsertRequest(record); err != nil {
		s.log.Error("insert request failed", "error", err)
	}

	for _, msg := range parsed.Messages {
		if err := s.db.InsertMessage(MessageRecord{
			RequestID: requestID,
			Role:      msg.Role,
			Content:   msg.Content,
			Name:      msg.Name,
			CreatedAt: createdAt,
		}); err != nil {
			s.log.Error("insert message failed", "error", err)
		}
	}

	upstreamURL, err := s.buildUpstreamURL(r.URL, target)
	if err != nil {
		http.Error(w, "invalid upstream url", http.StatusBadGateway)
		return
	}

	upstreamReq, err := http.NewRequestWithContext(r.Context(), r.Method, upstreamURL.String(), bytes.NewReader(body))
	if err != nil {
		http.Error(w, "failed to create upstream request", http.StatusInternalServerError)
		return
	}

	copyHeaders(upstreamReq.Header, r.Header)
	upstreamReq.Header.Set("Content-Type", r.Header.Get("Content-Type"))
	if err := s.applyAuth(upstreamReq.Header, target.provider); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	dbg.UpstreamRequest = &debugPayload{
		Timestamp: time.Now(),
		RequestID: requestID,
		SessionID: parsed.SessionID,
		Provider:  target.provider,
		Format:    target.format,
		Path:      upstreamURL.Path,
		Body:      decodeDebugBody(body),
		Headers:   sanitizeHeaders(upstreamReq.Header),
	}

	resp, err := s.client.Do(upstreamReq)
	if err != nil {
		http.Error(w, "upstream request failed", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	copyHeaders(w.Header(), resp.Header)
	w.WriteHeader(resp.StatusCode)

	data, upstreamResp := s.forwardResponse(w, resp.Body, requestID, parsed.SessionID, target.provider, resp.Header, resp.StatusCode)
	dbg.UpstreamResponse = &upstreamResp
	clientRespBody := decodeDebugBody(data)

	dbg.ClientResponse = &debugPayload{
		Timestamp: time.Now(),
		RequestID: requestID,
		SessionID: parsed.SessionID,
		Provider:  target.provider,
		Format:    target.format,
		Path:      "response",
		Headers:   sanitizeHeaders(w.Header()),
		Status:    resp.StatusCode,
		Body:      clientRespBody,
	}
	s.writeDebugRecord(parsed.SessionID, requestID, startedAt, dbg)

	if err := s.db.CompleteRequest(requestID, resp.StatusCode); err != nil {
		s.log.Error("complete request failed", "error", err)
	}

	elapsed := time.Since(startedAt)
	s.log.Info("provider response", "id", requestID, "session", parsed.SessionID, "provider", target.provider, "status", resp.StatusCode, "duration_ms", elapsed.Milliseconds())
	if err := s.appendLogLine(parsed.SessionID, proxyLogEntry{
		Timestamp:  time.Now(),
		Event:      "provider_response",
		RequestID:  requestID,
		Provider:   target.provider,
		Status:     resp.StatusCode,
		DurationMs: elapsed.Milliseconds(),
	}); err != nil {
		s.log.Error("write proxy log failed", "error", err)
	}

	time.Sleep(3 * time.Second)
}

func (s *ProxyServer) parseRequest(body []byte, header http.Header, format string) ParsedRequest {
	switch format {
	case "openai":
		parsed, ok := parseOpenAIRequest(body)
		if ok {
			headerID := firstHeader(header, "x-opencode-session-id", "x-session-id", "opencode-session-id")
			if headerID != "" {
				parsed.SessionID = headerID
			}
			return parsed
		}
	case "gemini":
		parsed, ok := parseGeminiRequest(body)
		if ok {
			headerID := firstHeader(header, "x-opencode-session-id", "x-session-id", "opencode-session-id")
			if headerID != "" {
				parsed.SessionID = headerID
			}
			return parsed
		}
	}

	return ParsedRequest{SessionID: firstHeader(header, "x-opencode-session-id", "x-session-id", "opencode-session-id")}
}

func (s *ProxyServer) buildUpstreamURL(original *url.URL, target upstreamTarget) (*url.URL, error) {
	base := s.upstreamBaseURL(target.provider)
	if base == "" {
		return nil, fmt.Errorf("missing upstream base url")
	}

	upstream, err := url.Parse(base)
	if err != nil {
		return nil, err
	}

	upstream.Path = strings.TrimSuffix(upstream.Path, "/") + target.path
	upstream.RawQuery = original.RawQuery
	return upstream, nil
}

type upstreamTarget struct {
	provider string
	format   string
	path     string
}

func (s *ProxyServer) resolveTarget(path string) (upstreamTarget, bool) {
	if strings.HasPrefix(path, "/v1/openai") {
		return upstreamTarget{
			provider: "openai",
			format:   "openai",
			path:     trimPrefixPath(path, "/v1/openai"),
		}, true
	}
	if strings.HasPrefix(path, "/v1/gemini") {
		return upstreamTarget{
			provider: "gemini",
			format:   "gemini",
			path:     "/v1beta" + trimPrefixPath(path, "/v1/gemini"),
		}, true
	}
	if strings.HasPrefix(path, "/v1beta") {
		return upstreamTarget{
			provider: "gemini",
			format:   "gemini",
			path:     path,
		}, true
	}
	if strings.HasPrefix(path, "/chat/completions") {
		return upstreamTarget{
			provider: "openai",
			format:   "openai",
			path:     path,
		}, true
	}
	return upstreamTarget{}, false
}

func trimPrefixPath(path string, prefix string) string {
	trimmed := strings.TrimPrefix(path, prefix)
	if trimmed == "" {
		return "/"
	}
	if !strings.HasPrefix(trimmed, "/") {
		return "/" + trimmed
	}
	return trimmed
}

func buildFingerprint(sessionID string, path string, body []byte) string {
	sum := sha256.Sum256(body)
	return sessionID + ":" + path + ":" + hex.EncodeToString(sum[:])
}

func (s *ProxyServer) getSessionID(userTurns []string) string {
	if len(userTurns) == 0 {
		return uuid.New().String()
	}

	cur := hashUserTurns(userTurns)

	s.mu.Lock()
	defer s.mu.Unlock()

	// multi-turn: look up by all previous user turns
	if len(userTurns) >= 2 {
		prev := hashUserTurns(userTurns[:len(userTurns)-1])
		if existing, ok := s.sessionIndex[prev]; ok {
			s.sessionIndex[cur] = existing
			return existing
		}
	}

	// first turn or no match: new session
	sessionID := uuid.New().String()
	s.sessionIndex[cur] = sessionID
	return sessionID
}

func hashUserTurns(turns []string) string {
	hasher := sha1.New()
	for _, turn := range turns {
		_, _ = hasher.Write([]byte(turn))
		_, _ = hasher.Write([]byte{0})
	}
	return hex.EncodeToString(hasher.Sum(nil))
}

func (s *ProxyServer) acquireFingerprint(fingerprint string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.inflight[fingerprint]; exists {
		return false
	}
	s.inflight[fingerprint] = struct{}{}
	return true
}

func (s *ProxyServer) releaseFingerprint(fingerprint string) {
	s.mu.Lock()
	delete(s.inflight, fingerprint)
	s.mu.Unlock()
}

type debugPayload struct {
	Timestamp time.Time         `json:"timestamp"`
	RequestID string            `json:"requestId"`
	SessionID string            `json:"sessionId"`
	Provider  string            `json:"provider"`
	Format    string            `json:"format"`
	Path      string            `json:"path"`
	Body      any               `json:"body"`
	Headers   map[string]string `json:"headers,omitempty"`
	Status    int               `json:"status,omitempty"`
}

type debugRecord struct {
	ClientRequest    *debugPayload `json:"client-request,omitempty"`
	UpstreamRequest  *debugPayload `json:"upstream-request,omitempty"`
	UpstreamResponse *debugPayload `json:"upstream-response,omitempty"`
	ClientResponse   *debugPayload `json:"client-response,omitempty"`
}

type proxyLogEntry struct {
	Timestamp  time.Time `json:"timestamp"`
	Event      string    `json:"event"`
	RequestID  string    `json:"requestId"`
	Provider   string    `json:"provider"`
	Method     string    `json:"method,omitempty"`
	Path       string    `json:"path,omitempty"`
	Model      string    `json:"model,omitempty"`
	Messages   int       `json:"messages,omitempty"`
	Bytes      int       `json:"bytes,omitempty"`
	Status     int       `json:"status,omitempty"`
	DurationMs int64     `json:"durationMs,omitempty"`
	Delta      string    `json:"delta,omitempty"`
	Upstream   bool      `json:"upstream,omitempty"`
}

func (s *ProxyServer) writeDebugRecord(sessionID, requestID string, startedAt time.Time, rec debugRecord) {
	if !s.cfg.Debug {
		return
	}

	folder := filepath.Join("debug", sessionID)
	if err := os.MkdirAll(folder, 0o755); err != nil {
		s.log.Error("create debug dir failed", "error", err)
		return
	}

	name := fmt.Sprintf("%s_%s.json", startedAt.Format("2006-01-02_15-04-05"), requestID)
	path := filepath.Join(folder, name)
	data, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		s.log.Error("marshal debug record failed", "error", err)
		return
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		s.log.Error("write debug record failed", "error", err)
	}
}

func decodeDebugBody(body []byte) any {
	var payload any
	if err := json.Unmarshal(body, &payload); err != nil {
		return string(body)
	}
	return payload
}

func sanitizeHeaders(header http.Header) map[string]string {
	result := make(map[string]string, len(header))
	for key, values := range header {
		if len(values) == 0 {
			continue
		}
		if isSensitiveHeader(key) {
			result[key] = ""
			continue
		}
		result[key] = strings.Join(values, ",")
	}
	return result
}

func isSensitiveHeader(key string) bool {
	lower := strings.ToLower(key)
	return lower == "authorization" || lower == "x-goog-api-key" || lower == "x-proxy-api-key"
}

func (s *ProxyServer) appendLogLine(sessionID string, entry proxyLogEntry) error {
	if sessionID == "" {
		return fmt.Errorf("missing session id")
	}
	path := filepath.Join(s.cfg.LogDir, sessionID, "proxy.json")
	data, err := json.Marshal(entry)
	if err != nil {
		return err
	}
	return appendJSONLine(path, data)
}

func appendJSONLine(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer file.Close()

	if _, err := file.Write(append(data, '\n')); err != nil {
		return err
	}
	return nil
}

func (s *ProxyServer) upstreamBaseURL(provider string) string {
	switch provider {
	case "openai":
		return s.cfg.OpenAIBaseURL
	case "gemini":
		return s.cfg.GeminiBaseURL
	case "copilot":
		return s.cfg.CopilotBaseURL
	default:
		return ""
	}
}

func (s *ProxyServer) applyAuth(header http.Header, provider string) error {
	override := header.Get("x-proxy-api-key")
	header.Del("x-proxy-api-key")

	switch provider {
	case "openai":
		key := firstNonEmpty(override, s.cfg.OpenAIAPIKey)
		if key == "" {
			return fmt.Errorf("missing OpenAI API key")
		}
		header.Del("x-goog-api-key")
		header.Set("Authorization", "Bearer "+key)
	case "gemini":
		key := firstNonEmpty(override, s.cfg.GeminiAPIKey)
		if key == "" {
			return fmt.Errorf("missing Gemini API key")
		}
		header.Del("Authorization")
		header.Set("x-goog-api-key", key)
	case "copilot":
		key := firstNonEmpty(override, s.cfg.CopilotAPIKey)
		if key == "" {
			return fmt.Errorf("missing Copilot API key")
		}
		header.Del("x-goog-api-key")
		header.Set("Authorization", "Bearer "+key)
	default:
		return fmt.Errorf("invalid provider")
	}
	return nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

var configTemplate = template.Must(template.New("config").Parse(`<!doctype html>
<html lang="en">
  <head>
    <meta charset="utf-8" />
    <meta name="viewport" content="width=device-width, initial-scale=1" />
    <title>opencode proxy config</title>
    <style>
      body {
        font-family: system-ui, -apple-system, BlinkMacSystemFont, "Segoe UI", sans-serif;
        margin: 2rem;
        color: #0f172a;
      }
      main {
        max-width: 720px;
      }
      label {
        display: block;
        font-weight: 600;
        margin-top: 1rem;
      }
      input {
        width: 100%;
        padding: 0.6rem 0.8rem;
        margin-top: 0.4rem;
        border: 1px solid #cbd5f5;
        border-radius: 8px;
      }
      button {
        margin-top: 0.4rem;
        padding: 0.5rem 1rem;
        border: 1px solid #0f172a;
        border-radius: 8px;
        background: #fff;
        color: #0f172a;
        cursor: pointer;
      }
      .hint {
        color: #475569;
        font-size: 0.9rem;
        margin-top: 0.3rem;
      }
      .field {
        display: grid;
        gap: 0.3rem;
      }
      code {
        background: #f1f5f9;
        padding: 0.1rem 0.3rem;
        border-radius: 4px;
      }
    </style>
  </head>
  <body>
    <main>
      <h1>opencode proxy</h1>
      <p>Routing is based on the request path prefix.</p>
      <ul>
        <li><code>/v1/openai</code> → OpenAI upstream</li>
        <li><code>/v1/gemini</code> → Gemini native API (rewrites to <code>/v1beta</code>)</li>
        <li><code>/v1beta</code> → Gemini native API</li>
      </ul>

      <label for="openai-key">OpenAI key (.env)</label>
      <div class="field">
        <input id="openai-key" type="password" value="{{.OpenAIKey}}" readonly />
        <button type="button" data-target="openai-key">Show key</button>
      </div>

      <label for="gemini-key">Gemini key (.env)</label>
      <div class="field">
        <input id="gemini-key" type="password" value="{{.GeminiKey}}" readonly />
        <button type="button" data-target="gemini-key">Show key</button>
      </div>

      <label for="copilot-key">Copilot key (.env)</label>
      <div class="field">
        <input id="copilot-key" type="password" value="{{.CopilotKey}}" readonly />
        <button type="button" data-target="copilot-key">Show key</button>
      </div>

      <div class="hint">Keys are masked by default. Use “Show key” to reveal.</div>
    </main>

    <script>
      document.querySelectorAll("button[data-target]").forEach((button) => {
        button.addEventListener("click", () => {
          const target = document.getElementById(button.dataset.target);
          if (!target) return;
          if (target.type === "password") {
            target.type = "text";
            button.textContent = "Hide key";
          } else {
            target.type = "password";
            button.textContent = "Show key";
          }
        });
      });
    </script>
  </body>
</html>`))

var testTemplate = template.Must(template.New("test").Parse(`<!doctype html>
<html lang="en">
  <head>
    <meta charset="utf-8" />
    <meta name="viewport" content="width=device-width, initial-scale=1" />
    <title>opencode proxy test</title>
    <style>
      body {
        font-family: system-ui, -apple-system, BlinkMacSystemFont, "Segoe UI", sans-serif;
        margin: 0;
        background: #f8fafc;
        color: #0f172a;
      }
      main {
        max-width: 980px;
        margin: 0 auto;
        padding: 2.5rem 1.5rem 4rem;
      }
      h1 {
        margin-bottom: 0.4rem;
      }
      .card {
        background: #fff;
        border: 1px solid #e2e8f0;
        border-radius: 16px;
        padding: 1.5rem;
        box-shadow: 0 6px 20px rgba(15, 23, 42, 0.08);
      }
      .stack {
        display: grid;
        gap: 1.25rem;
      }
      label {
        display: block;
        font-weight: 600;
      }
      input,
      select,
      textarea {
        width: 100%;
        padding: 0.65rem 0.8rem;
        margin-top: 0.4rem;
        border: 1px solid #cbd5f5;
        border-radius: 10px;
        font-family: inherit;
        background: #fff;
      }
      textarea {
        min-height: 120px;
      }
      button {
        margin-top: 0.6rem;
        padding: 0.7rem 1.4rem;
        border: none;
        border-radius: 10px;
        background: #0f172a;
        color: #fff;
        cursor: pointer;
        font-weight: 600;
      }
      .inline {
        display: flex;
        gap: 0.6rem;
        align-items: center;
      }
      .inline button {
        margin-top: 0.4rem;
        padding: 0.55rem 1rem;
        background: #fff;
        color: #0f172a;
        border: 1px solid #0f172a;
      }
      pre {
        background: #0f172a;
        color: #f8fafc;
        padding: 1rem;
        border-radius: 12px;
        overflow-x: auto;
        white-space: pre-wrap;
        word-break: break-word;
        min-height: 80px;
      }
      .grid {
        display: grid;
        gap: 1rem;
        grid-template-columns: repeat(auto-fit, minmax(220px, 1fr));
      }
      .status {
        margin-top: 0.4rem;
        font-weight: 600;
      }
      .hint {
        color: #475569;
        font-size: 0.9rem;
      }
    </style>
  </head>
  <body>
    <main>
      <h1>Test Request</h1>
      <p class="hint">Single request tester with SSE streaming for OpenAI or Gemini.</p>

      <section class="card stack">
        <div class="grid">
          <div>
            <label for="provider">Provider</label>
            <select id="provider"></select>
          </div>
          <div>
            <label for="model">Model</label>
            <select id="model"></select>
          </div>
          <div>
            <label for="temperature">Temperature</label>
            <input id="temperature" value="0.7" />
          </div>
          <div>
            <label for="top-p">Top P</label>
            <input id="top-p" value="0.9" />
          </div>
        </div>

        <div class="grid">
          <div>
            <label for="api-key">API Key</label>
            <div class="inline">
              <input id="api-key" type="password" value="{{.OpenAIKey}}" />
              <button type="button" id="toggle-key">Show</button>
            </div>
            <div class="hint">Defaults to the .env key for the selected provider.</div>
          </div>
          <div>
            <label for="mock-mode">Mock mode</label>
            <div class="inline">
              <input id="mock-mode" type="checkbox" />
              <span class="hint">Route to /test/mock/gemini</span>
            </div>
          </div>
        </div>

        <div>
          <label for="system">System prompt</label>
          <textarea id="system" placeholder="You are a helpful assistant."></textarea>
        </div>

        <div>
          <label for="user">User prompt</label>
          <textarea id="user" placeholder="Say hello."></textarea>
        </div>

        <div>
          <button id="send">Send</button>
          <div class="status" id="status"></div>
        </div>
      </section>

      <section class="stack" style="margin-top: 1.5rem;">
        <div class="card">
          <label>Request JSON</label>
          <pre id="request"></pre>
        </div>
        <div class="card">
          <label>Response (streamed text)</label>
          <pre id="response"></pre>
        </div>
        <div class="card">
          <label>Raw SSE events</label>
          <pre id="events"></pre>
        </div>
      </section>
    </main>

    <script>
      const providerEl = document.getElementById("provider");
      const modelEl = document.getElementById("model");
      const temperatureEl = document.getElementById("temperature");
      const topPEl = document.getElementById("top-p");
      const systemEl = document.getElementById("system");
      const userEl = document.getElementById("user");
      const apiKeyEl = document.getElementById("api-key");
      const mockEl = document.getElementById("mock-mode");
      const toggleKeyEl = document.getElementById("toggle-key");
      const sendEl = document.getElementById("send");
      const statusEl = document.getElementById("status");
      const requestEl = document.getElementById("request");
      const responseEl = document.getElementById("response");
      const eventsEl = document.getElementById("events");

      const defaults = {
        openai: "gpt-4o",
        gemini: "gemini-2.5-pro",
      };

      const providerKeys = {
        openai: "{{.OpenAIKey}}",
        gemini: "{{.GeminiKey}}",
      };

      const modelOptions = {
        openai: ["", "gpt-4o", "gpt-4o-mini", "gpt-4.1"],
        gemini: ["", "gemini-2.5-pro", "gemini-2.5-flash", "gemini-2.5-flash-lite", "gemini-3.0-flash"],
      };

      function toNumber(value) {
        const parsed = Number(value);
        if (Number.isNaN(parsed)) return undefined;
        return parsed;
      }

      function valueOrPlaceholder(element) {
        const value = element.value.trim();
        if (value !== "") return value;
        return (element.placeholder || "").trim();
      }

      function buildOpenAIRequest(model) {
        const system = valueOrPlaceholder(systemEl);
        const user = valueOrPlaceholder(userEl);
        const messages = [];
        if (system) messages.push({ role: "system", content: system });
        if (user) messages.push({ role: "user", content: user });
        return {
          model,
          stream: true,
          temperature: toNumber(temperatureEl.value),
          top_p: toNumber(topPEl.value),
          messages,
        };
      }

      function buildGeminiRequest() {
        const system = valueOrPlaceholder(systemEl);
        const user = valueOrPlaceholder(userEl);
        const payload = {
          contents: [],
          generationConfig: {
            temperature: toNumber(temperatureEl.value),
            topP: toNumber(topPEl.value),
          },
        };
        if (system) {
          payload.systemInstruction = { role: "system", parts: [{ text: system }] };
        }
        if (user) {
          payload.contents.push({ role: "user", parts: [{ text: user }] });
        }
        return payload;
      }

      function parseOpenAIEvent(line) {
        if (!line.startsWith("data:")) return "";
        const json = line.replace(/^data:\s*/, "");
        if (!json || json === "[DONE]") return "";
        try {
          const payload = JSON.parse(json);
          return payload.choices?.[0]?.delta?.content ?? "";
        } catch (error) {
          return "";
        }
      }

      function parseGeminiEvent(line) {
        if (!line.startsWith("data:")) return "";
        const json = line.replace(/^data:\s*/, "");
        if (!json || json === "[DONE]") return "";
        try {
          const payload = JSON.parse(json);
          return payload.candidates?.[0]?.content?.parts?.map((part) => part.text ?? "").join("") ?? "";
        } catch (error) {
          return "";
        }
      }

      function populateProviders() {
        providerEl.innerHTML = "";
        ["openai", "gemini"].forEach((provider) => {
          const option = document.createElement("option");
          option.value = provider;
          option.textContent = provider;
          providerEl.appendChild(option);
        });
      }

      function populateModels(provider) {
        modelEl.innerHTML = "";
        const options = modelOptions[provider] || [""];
        options.forEach((model) => {
          const option = document.createElement("option");
          option.value = model;
          option.textContent = model === "" ? "default" : model;
          modelEl.appendChild(option);
        });
      }

      function syncApiKey(provider) {
        apiKeyEl.value = providerKeys[provider] || "";
        apiKeyEl.type = "password";
        toggleKeyEl.textContent = "Show";
      }

      async function sendRequest() {
        statusEl.textContent = "Sending...";
        responseEl.textContent = "";
        eventsEl.textContent = "";

        const provider = providerEl.value;
        const isGemini = provider === "gemini";
        const selectedModel = modelEl.value.trim();
        const model = selectedModel === "" ? defaults[provider] : selectedModel;
        const payload = isGemini ? buildGeminiRequest() : buildOpenAIRequest(model);
        const useMock = mockEl.checked && isGemini;
        const endpoint = useMock
          ? "/test/mock/gemini"
          : isGemini
            ? "/v1/gemini/models/" + model + ":streamGenerateContent?alt=sse"
            : "/v1/openai/chat/completions";

        requestEl.textContent = JSON.stringify(payload, null, 2);

        const headers = { "Content-Type": "application/json" };
        if (apiKeyEl.value.trim() !== "") {
          headers["x-proxy-api-key"] = apiKeyEl.value.trim();
        }

        const response = await fetch(endpoint, {
          method: "POST",
          headers,
          body: JSON.stringify(payload),
        });

        statusEl.textContent = "Status: " + response.status;
        if (!response.body) {
          responseEl.textContent = "No response body";
          return;
        }

        const reader = response.body.getReader();
        const decoder = new TextDecoder();
        let buffer = "";

        while (true) {
          const { value, done } = await reader.read();
          if (done) break;
          buffer += decoder.decode(value, { stream: true });
          const lines = buffer.split("\n");
          buffer = lines.pop() ?? "";
          for (const line of lines) {
            if (!line.trim()) continue;
            eventsEl.textContent += line + "\n";
            const text = isGemini ? parseGeminiEvent(line) : parseOpenAIEvent(line);
            if (text) responseEl.textContent += text;
          }
        }
      }

      toggleKeyEl.addEventListener("click", () => {
        if (apiKeyEl.type === "password") {
          apiKeyEl.type = "text";
          toggleKeyEl.textContent = "Hide";
        } else {
          apiKeyEl.type = "password";
          toggleKeyEl.textContent = "Show";
        }
      });

      providerEl.addEventListener("change", () => {
        populateModels(providerEl.value);
        syncApiKey(providerEl.value);
        if (providerEl.value !== "gemini") {
          mockEl.checked = false;
        }
      });

      sendEl.addEventListener("click", () => {
        sendRequest().catch((error) => {
          statusEl.textContent = "Error: " + error.message;
        });
      });

      populateProviders();
      populateModels("openai");
      syncApiKey("openai");
      mockEl.checked = false;
    </script>
  </body>
</html>`))

func (s *ProxyServer) handleConfigPage(w http.ResponseWriter) {
	state := ConfigState{
		OpenAIKey:  s.cfg.OpenAIAPIKey,
		GeminiKey:  s.cfg.GeminiAPIKey,
		CopilotKey: s.cfg.CopilotAPIKey,
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := configTemplate.Execute(w, state); err != nil {
		http.Error(w, "failed to render page", http.StatusInternalServerError)
	}
}

func (s *ProxyServer) handleTestPage(w http.ResponseWriter) {
	state := ConfigState{
		OpenAIKey:  s.cfg.OpenAIAPIKey,
		GeminiKey:  s.cfg.GeminiAPIKey,
		CopilotKey: s.cfg.CopilotAPIKey,
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := testTemplate.Execute(w, state); err != nil {
		http.Error(w, "failed to render page", http.StatusInternalServerError)
	}
}

func (s *ProxyServer) handleGeminiMock(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "stream unsupported", http.StatusInternalServerError)
		return
	}

	chunks := []string{
		`data: {"candidates": [{"content": {"parts": [{"text": "Hello"}],"role": "model"},"index": 0}],"usageMetadata": {"promptTokenCount": 11,"candidatesTokenCount": 1,"totalTokenCount": 12,"promptTokensDetails": [{"modality": "TEXT","tokenCount": 11}]},"modelVersion": "gemini-2.5-flash-lite","responseId": "mock-response"}`,
		`data: {"candidates": [{"content": {"parts": [{"text": "! How can I help you today?"}],"role": "model"},"finishReason": "STOP","index": 0}],"usageMetadata": {"promptTokenCount": 11,"candidatesTokenCount": 9,"totalTokenCount": 20,"promptTokensDetails": [{"modality": "TEXT","tokenCount": 11}]},"modelVersion": "gemini-2.5-flash-lite","responseId": "mock-response"}`,
		"data: [DONE]",
	}

	for _, chunk := range chunks {
		_, _ = io.WriteString(w, chunk+"\n\n")
		flusher.Flush()
		time.Sleep(120 * time.Millisecond)
	}
}


func (s *ProxyServer) forwardResponse(w http.ResponseWriter, body io.Reader, requestID string, sessionID string, provider string, headers http.Header, status int) ([]byte, debugPayload) {
	data, err := io.ReadAll(body)
	if err != nil {
		http.Error(w, "failed to read upstream", http.StatusBadGateway)
		return nil, debugPayload{}
	}

	_, _ = w.Write(data)

	if delta := s.extractFullResponseDelta(data, provider); delta != "" {
		_ = s.appendLogLine(sessionID, proxyLogEntry{
			Timestamp: time.Now(),
			Event:     "response_delta",
			RequestID: requestID,
			Provider:  provider,
			Delta:     delta,
		})
		_ = s.db.InsertResponseDelta(ResponseDelta{
			RequestID: requestID,
			Seq:       1,
			Delta:     delta,
			CreatedAt: time.Now(),
		})
	}

	if usage, ok := s.extractUsageFromResponse(data, requestID, provider); ok {
		_ = s.db.UpsertUsage(usage)
	}

	return data, debugPayload{
		Timestamp: time.Now(),
		RequestID: requestID,
		SessionID: sessionID,
		Provider:  provider,
		Format:    provider,
		Path:      "response",
		Headers:   sanitizeHeaders(headers),
		Status:    status,
		Body:      decodeDebugBody(data),
	}
}

func (s *ProxyServer) extractDelta(line string, provider string) string {
	trimmed := strings.TrimSpace(line)
	if strings.HasPrefix(trimmed, "data:") {
		trimmed = strings.TrimSpace(strings.TrimPrefix(trimmed, "data:"))
	}

	if trimmed == "" || trimmed == "[DONE]" {
		return ""
	}

	if provider == "gemini" {
		return parseGeminiDelta([]byte(trimmed))
	}

	return parseOpenAIDelta([]byte(trimmed))
}

func (s *ProxyServer) extractUsage(line string, requestID string, provider string) (UsageRecord, bool) {
	trimmed := strings.TrimSpace(line)
	if strings.HasPrefix(trimmed, "data:") {
		trimmed = strings.TrimSpace(strings.TrimPrefix(trimmed, "data:"))
	}
	if trimmed == "" || trimmed == "[DONE]" {
		return UsageRecord{}, false
	}

	var (
		usage UsageRecord
		ok    bool
	)
	if provider == "gemini" {
		usage, ok = parseGeminiUsage([]byte(trimmed))
	} else {
		usage, ok = parseOpenAIUsage([]byte(trimmed))
	}
	if !ok {
		return UsageRecord{}, false
	}
	usage.RequestID = requestID
	return usage, true
}

func (s *ProxyServer) extractFullResponseDelta(data []byte, provider string) string {
	if provider == "gemini" {
		return parseGeminiFullDelta(data)
	}
	return parseOpenAIFullDelta(data)
}

func (s *ProxyServer) extractUsageFromResponse(data []byte, requestID string, provider string) (UsageRecord, bool) {
	var (
		usage UsageRecord
		ok    bool
	)
	if provider == "gemini" {
		usage, ok = parseGeminiUsage(data)
	} else {
		usage, ok = parseOpenAIUsage(data)
	}
	if !ok {
		return UsageRecord{}, false
	}
	usage.RequestID = requestID
	return usage, true
}

func copyHeaders(dst http.Header, src http.Header) {
	for key, values := range src {
		if strings.EqualFold(key, "Host") {
			continue
		}
		if strings.EqualFold(key, "Content-Length") {
			continue
		}
		for _, value := range values {
			dst.Add(key, value)
		}
	}
}

func firstHeader(header http.Header, keys ...string) string {
	for _, key := range keys {
		if value := header.Get(key); value != "" {
			return value
		}
	}
	return ""
}
