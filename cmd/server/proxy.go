package main

import (
	"bufio"
	"bytes"
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
	"time"

	"github.com/google/uuid"
)

type ProxyServer struct {
	cfg    Config
	db     *Database
	client *http.Client
	log    *slog.Logger
}

func NewProxyServer(cfg Config, db *Database, log *slog.Logger) *ProxyServer {
	return &ProxyServer{
		cfg: cfg,
		db:  db,
		client: &http.Client{
			Timeout: cfg.RequestTimeout,
		},
		log: log,
	}
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
	}

	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	s.handleProxy(w, r)
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
		parsed.SessionID = uuid.New().String()
	}

	requestID := uuid.New().String()
	createdAt := time.Now()
	startedAt := time.Now()

	s.log.Info("request", "id", requestID, "session", parsed.SessionID, "provider", target.provider, "method", r.Method, "path", r.URL.Path, "model", parsed.Model, "messages", len(parsed.Messages), "bytes", len(body))
	s.appendLogLine(parsed.SessionID, fmt.Sprintf("%s request id=%s provider=%s method=%s path=%s model=%s messages=%d bytes=%d", startedAt.Format(time.RFC3339), requestID, target.provider, r.Method, r.URL.Path, parsed.Model, len(parsed.Messages), len(body)))
	s.writeDebugPayload(debugPayload{
		Timestamp: startedAt,
		RequestID: requestID,
		SessionID: parsed.SessionID,
		Provider:  target.provider,
		Format:    target.format,
		Path:      r.URL.Path,
		Direction: "client",
		Body:      decodeDebugBody(body),
	})

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

	s.writeDebugPayload(debugPayload{
		Timestamp: time.Now(),
		RequestID: requestID,
		SessionID: parsed.SessionID,
		Provider:  target.provider,
		Format:    target.format,
		Path:      upstreamURL.Path,
		Direction: "upstream",
		Body:      decodeDebugBody(body),
	})

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

	resp, err := s.client.Do(upstreamReq)
	if err != nil {
		http.Error(w, "upstream request failed", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	copyHeaders(w.Header(), resp.Header)
	w.WriteHeader(resp.StatusCode)

	if parsed.Stream || strings.Contains(resp.Header.Get("Content-Type"), "text/event-stream") {
		s.streamResponse(w, resp.Body, requestID, parsed.SessionID, target.provider)
	} else {
		s.forwardResponse(w, resp.Body, requestID, parsed.SessionID, target.provider)
	}

	if err := s.db.CompleteRequest(requestID, resp.StatusCode); err != nil {
		s.log.Error("complete request failed", "error", err)
	}

	elapsed := time.Since(startedAt)
	s.log.Info("response", "id", requestID, "session", parsed.SessionID, "provider", target.provider, "status", resp.StatusCode, "duration_ms", elapsed.Milliseconds())
	s.appendLogLine(parsed.SessionID, fmt.Sprintf("%s response id=%s provider=%s status=%d duration_ms=%d", time.Now().Format(time.RFC3339), requestID, target.provider, resp.StatusCode, elapsed.Milliseconds()))

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

type debugPayload struct {
	Timestamp time.Time `json:"timestamp"`
	RequestID string    `json:"requestId"`
	SessionID string    `json:"sessionId"`
	Provider  string    `json:"provider"`
	Format    string    `json:"format"`
	Path      string    `json:"path"`
	Direction string    `json:"direction"`
	Body      any       `json:"body"`
}

func (s *ProxyServer) writeDebugPayload(payload debugPayload) {
	if !s.cfg.Debug {
		return
	}
	if err := os.MkdirAll("debug", 0o755); err != nil {
		s.log.Error("create debug dir failed", "error", err)
		return
	}

	name := fmt.Sprintf("%s-%s.json", payload.RequestID, payload.Direction)
	path := filepath.Join("debug", name)
	data, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		s.log.Error("marshal debug payload failed", "error", err)
		return
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		s.log.Error("write debug payload failed", "error", err)
	}
}

func decodeDebugBody(body []byte) any {
	var payload any
	if err := json.Unmarshal(body, &payload); err != nil {
		return string(body)
	}
	return payload
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
	switch provider {
	case "openai":
		if s.cfg.OpenAIAPIKey == "" {
			return fmt.Errorf("missing OpenAI API key")
		}
		header.Del("x-goog-api-key")
		header.Set("Authorization", "Bearer "+s.cfg.OpenAIAPIKey)
	case "gemini":
		if s.cfg.GeminiAPIKey == "" {
			return fmt.Errorf("missing Gemini API key")
		}
		header.Del("Authorization")
		header.Set("x-goog-api-key", s.cfg.GeminiAPIKey)
	case "copilot":
		if s.cfg.CopilotAPIKey == "" {
			return fmt.Errorf("missing Copilot API key")
		}
		header.Del("x-goog-api-key")
		header.Set("Authorization", "Bearer "+s.cfg.CopilotAPIKey)
	default:
		return fmt.Errorf("invalid provider")
	}
	return nil
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

func (s *ProxyServer) streamResponse(w http.ResponseWriter, body io.Reader, requestID string, sessionID string, provider string) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		s.forwardResponse(w, body, requestID, sessionID, provider)
		return
	}

	logFile, closeFile := s.openLogFile(sessionID)
	defer closeFile()

	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	seq := 0

	for scanner.Scan() {
		line := scanner.Text()
		_, _ = io.WriteString(w, line+"\n")
		flusher.Flush()

		if delta := s.extractDelta(line, provider); delta != "" {
			seq++
			if logFile != nil {
				_, _ = logFile.WriteString(delta)
			}
			_ = s.db.InsertResponseDelta(ResponseDelta{
				RequestID: requestID,
				Seq:       seq,
				Delta:     delta,
				CreatedAt: time.Now(),
			})
		}

		if usage, ok := s.extractUsage(line, requestID, provider); ok {
			_ = s.db.UpsertUsage(usage)
		}
	}
}

func (s *ProxyServer) forwardResponse(w http.ResponseWriter, body io.Reader, requestID string, sessionID string, provider string) {
	data, err := io.ReadAll(body)
	if err != nil {
		http.Error(w, "failed to read upstream", http.StatusBadGateway)
		return
	}

	_, _ = w.Write(data)

	if delta := s.extractFullResponseDelta(data, provider); delta != "" {
		logFile, closeFile := s.openLogFile(sessionID)
		defer closeFile()

		if logFile != nil {
			_, _ = logFile.WriteString(delta)
		}
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
	if provider == "gemini" {
		return UsageRecord{}, false
	}
	trimmed := strings.TrimSpace(line)
	if strings.HasPrefix(trimmed, "data:") {
		trimmed = strings.TrimSpace(strings.TrimPrefix(trimmed, "data:"))
	}
	if trimmed == "" || trimmed == "[DONE]" {
		return UsageRecord{}, false
	}

	usage, ok := parseOpenAIUsage([]byte(trimmed))
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
	if provider == "gemini" {
		return UsageRecord{}, false
	}
	usage, ok := parseOpenAIUsage(data)
	if !ok {
		return UsageRecord{}, false
	}
	usage.RequestID = requestID
	return usage, true
}

func (s *ProxyServer) openLogFile(sessionID string) (*os.File, func()) {
	if sessionID == "" {
		return nil, func() {}
	}

	if err := os.MkdirAll(s.cfg.LogDir, 0o755); err != nil {
		s.log.Error("create log dir failed", "error", err)
		return nil, func() {}
	}

	path := filepath.Join(s.cfg.LogDir, sessionID+".log")
	file, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		s.log.Error("open log file failed", "error", err)
		return nil, func() {}
	}

	return file, func() { _ = file.Close() }
}

func (s *ProxyServer) appendLogLine(sessionID string, line string) {
	file, closeFile := s.openLogFile(sessionID)
	defer closeFile()
	if file == nil {
		return
	}
	_, _ = file.WriteString(line + "\n")
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
