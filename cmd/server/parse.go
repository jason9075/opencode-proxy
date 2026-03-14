package main

import (
	"encoding/json"
	"strings"
)

type OpenAIRequest struct {
	Model       string          `json:"model"`
	Messages    []OpenAIMessage `json:"messages"`
	Stream      bool            `json:"stream"`
	Temperature *float64        `json:"temperature"`
	TopP        *float64        `json:"top_p"`
	MaxTokens   *int            `json:"max_tokens"`
	User        string          `json:"user"`
	SessionID   string          `json:"session_id"`
	Metadata    map[string]any  `json:"metadata"`
}

type OpenAIMessage struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
	Name    string          `json:"name"`
}

type GeminiRequest struct {
	Contents          []GeminiContent         `json:"contents"`
	SystemInstruction *GeminiContent          `json:"systemInstruction"`
	GenerationConfig  *GeminiGenerationConfig `json:"generationConfig"`
}

type GeminiContent struct {
	Role  string       `json:"role"`
	Parts []GeminiPart `json:"parts"`
}

type GeminiPart struct {
	Text string `json:"text"`
}

type GeminiGenerationConfig struct {
	Temperature     *float64 `json:"temperature"`
	TopP            *float64 `json:"topP"`
	MaxOutputTokens *int     `json:"maxOutputTokens"`
}

func parseOpenAIRequest(body []byte) (ParsedRequest, bool) {
	var req OpenAIRequest
	if err := json.Unmarshal(body, &req); err != nil {
		return ParsedRequest{}, false
	}

	messages := make([]LoggedMessage, 0, len(req.Messages))
	var userTurns []string
	for _, msg := range req.Messages {
		content := parseOpenAIContent(msg.Content)
		messages = append(messages, LoggedMessage{
			Role:    msg.Role,
			Content: content,
			Name:    msg.Name,
		})
		if msg.Role == "user" {
			userTurns = append(userTurns, content)
		}
	}

	sessionID := req.SessionID
	if sessionID == "" && req.Metadata != nil {
		if value, ok := req.Metadata["session_id"].(string); ok {
			sessionID = value
		}
		if value, ok := req.Metadata["sessionID"].(string); ok {
			sessionID = value
		}
	}

	return ParsedRequest{
		SessionID:   sessionID,
		Model:       req.Model,
		Stream:      req.Stream,
		User:        req.User,
		Temperature: req.Temperature,
		TopP:        req.TopP,
		MaxTokens:   req.MaxTokens,
		Messages:    messages,
		UserTurns:   userTurns,
	}, true
}

func parseOpenAIContent(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}

	if raw[0] == '"' {
		var text string
		if err := json.Unmarshal(raw, &text); err == nil {
			return text
		}
	}

	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(raw, &parts); err == nil {
		texts := make([]string, 0, len(parts))
		for _, part := range parts {
			if part.Text != "" {
				texts = append(texts, part.Text)
			}
		}
		return strings.Join(texts, "")
	}

	return ""
}

func parseGeminiRequest(body []byte) (ParsedRequest, bool) {
	var req GeminiRequest
	if err := json.Unmarshal(body, &req); err != nil {
		return ParsedRequest{}, false
	}

	messages := make([]LoggedMessage, 0, len(req.Contents))
	var userTurns []string
	if req.SystemInstruction != nil {
		messages = append(messages, LoggedMessage{
			Role:    "system",
			Content: joinGeminiParts(req.SystemInstruction.Parts),
		})
	}
	for _, content := range req.Contents {
		role := content.Role
		if role == "model" {
			role = "assistant"
		}
		messages = append(messages, LoggedMessage{
			Role:    role,
			Content: joinGeminiParts(content.Parts),
		})
		if role == "user" && len(content.Parts) > 0 {
			userTurns = append(userTurns, content.Parts[0].Text)
		}
	}

	parsed := ParsedRequest{
		Stream:    true,
		Messages:  messages,
		UserTurns: userTurns,
	}

	if req.GenerationConfig != nil {
		parsed.Temperature = req.GenerationConfig.Temperature
		parsed.TopP = req.GenerationConfig.TopP
		parsed.MaxTokens = req.GenerationConfig.MaxOutputTokens
	}

	return parsed, true
}

func joinGeminiParts(parts []GeminiPart) string {
	texts := make([]string, 0, len(parts))
	for _, part := range parts {
		if part.Text != "" {
			texts = append(texts, part.Text)
		}
	}
	return strings.Join(texts, "")
}
