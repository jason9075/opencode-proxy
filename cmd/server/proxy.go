package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
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
	if r.Method == http.MethodGet && r.URL.Path == "/healthz" {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
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

	parsed := s.parseRequest(body, r.Header)
	if parsed.SessionID == "" {
		parsed.SessionID = uuid.New().String()
	}

	requestID := uuid.New().String()
	createdAt := time.Now()

	record := RequestRecord{
		ID:          requestID,
		SessionID:   parsed.SessionID,
		Provider:    s.cfg.Provider,
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

	upstreamURL, err := s.buildUpstreamURL(r.URL)
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
	s.applyAuth(upstreamReq.Header)

	resp, err := s.client.Do(upstreamReq)
	if err != nil {
		http.Error(w, "upstream request failed", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	copyHeaders(w.Header(), resp.Header)
	w.WriteHeader(resp.StatusCode)

	if parsed.Stream || strings.Contains(resp.Header.Get("Content-Type"), "text/event-stream") {
		s.streamResponse(w, resp.Body, requestID, parsed.SessionID)
	} else {
		s.forwardResponse(w, resp.Body, requestID, parsed.SessionID)
	}

	if err := s.db.CompleteRequest(requestID, resp.StatusCode); err != nil {
		s.log.Error("complete request failed", "error", err)
	}
}

func (s *ProxyServer) parseRequest(body []byte, header http.Header) ParsedRequest {
	switch s.cfg.Provider {
	case "openai", "copilot":
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

func (s *ProxyServer) buildUpstreamURL(original *url.URL) (*url.URL, error) {
	base := s.upstreamBaseURL()
	if base == "" {
		return nil, fmt.Errorf("missing upstream base url")
	}

	target, err := url.Parse(base)
	if err != nil {
		return nil, err
	}

	target.Path = strings.TrimSuffix(target.Path, "/") + original.Path
	target.RawQuery = original.RawQuery
	return target, nil
}

func (s *ProxyServer) upstreamBaseURL() string {
	switch s.cfg.Provider {
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

func (s *ProxyServer) applyAuth(header http.Header) {
	switch s.cfg.Provider {
	case "openai":
		header.Set("Authorization", "Bearer "+s.cfg.OpenAIAPIKey)
	case "gemini":
		if s.cfg.GeminiAPIKey != "" {
			header.Set("x-goog-api-key", s.cfg.GeminiAPIKey)
		}
	case "copilot":
		header.Set("Authorization", "Bearer "+s.cfg.CopilotAPIKey)
	}
}

func (s *ProxyServer) streamResponse(w http.ResponseWriter, body io.Reader, requestID string, sessionID string) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		s.forwardResponse(w, body, requestID, sessionID)
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

		if delta := s.extractDelta(line); delta != "" {
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

		if usage, ok := s.extractUsage(line, requestID); ok {
			_ = s.db.UpsertUsage(usage)
		}
	}
}

func (s *ProxyServer) forwardResponse(w http.ResponseWriter, body io.Reader, requestID string, sessionID string) {
	data, err := io.ReadAll(body)
	if err != nil {
		http.Error(w, "failed to read upstream", http.StatusBadGateway)
		return
	}

	_, _ = w.Write(data)

	if delta := s.extractFullResponseDelta(data); delta != "" {
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

	if usage, ok := s.extractUsageFromResponse(data, requestID); ok {
		_ = s.db.UpsertUsage(usage)
	}
}

func (s *ProxyServer) extractDelta(line string) string {
	trimmed := strings.TrimSpace(line)
	if strings.HasPrefix(trimmed, "data:") {
		trimmed = strings.TrimSpace(strings.TrimPrefix(trimmed, "data:"))
	}

	if trimmed == "" || trimmed == "[DONE]" {
		return ""
	}

	if s.cfg.Provider == "gemini" {
		return parseGeminiDelta([]byte(trimmed))
	}

	return parseOpenAIDelta([]byte(trimmed))
}

func (s *ProxyServer) extractUsage(line string, requestID string) (UsageRecord, bool) {
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

func (s *ProxyServer) extractFullResponseDelta(data []byte) string {
	if s.cfg.Provider == "gemini" {
		return parseGeminiFullDelta(data)
	}
	return parseOpenAIFullDelta(data)
}

func (s *ProxyServer) extractUsageFromResponse(data []byte, requestID string) (UsageRecord, bool) {
	if s.cfg.Provider == "gemini" {
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
