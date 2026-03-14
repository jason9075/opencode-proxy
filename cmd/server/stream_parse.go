package main

import (
	"encoding/json"
	"strings"
)

type OpenAIChunk struct {
	Choices []struct {
		Delta struct {
			Content          string `json:"content"`
			ReasoningContent string `json:"reasoning_content"`
		} `json:"delta"`
		Message struct {
			Content          string `json:"content"`
			ReasoningContent string `json:"reasoning_content"`
		} `json:"message"`
	} `json:"choices"`
	Usage *struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
		TotalTokens      int `json:"total_tokens"`
		PromptTokensDetails *struct {
			CachedTokens int `json:"cached_tokens"`
		} `json:"prompt_tokens_details"`
	} `json:"usage"`
}

type GeminiChunk struct {
	Candidates []struct {
		Content struct {
			Parts []struct {
				Text    string `json:"text"`
				Thought bool   `json:"thought"`
			} `json:"parts"`
		} `json:"content"`
	} `json:"candidates"`
	UsageMetadata *struct {
		PromptTokenCount        int `json:"promptTokenCount"`
		CandidatesTokenCount    int `json:"candidatesTokenCount"`
		TotalTokenCount         int `json:"totalTokenCount"`
		CachedContentTokenCount int `json:"cachedContentTokenCount"`
	} `json:"usageMetadata"`
}

type OpenAIResponse struct {
	Choices []struct {
		Message struct {
			Content          string `json:"content"`
			ReasoningContent string `json:"reasoning_content"`
		} `json:"message"`
	} `json:"choices"`
	Usage *struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
		TotalTokens      int `json:"total_tokens"`
		PromptTokensDetails *struct {
			CachedTokens int `json:"cached_tokens"`
		} `json:"prompt_tokens_details"`
	} `json:"usage"`
}

type GeminiResponse struct {
	Candidates []struct {
		Content struct {
			Parts []struct {
				Text    string `json:"text"`
				Thought bool   `json:"thought"`
			} `json:"parts"`
		} `json:"content"`
	} `json:"candidates"`
	UsageMetadata *struct {
		PromptTokenCount        int `json:"promptTokenCount"`
		CandidatesTokenCount    int `json:"candidatesTokenCount"`
		TotalTokenCount         int `json:"totalTokenCount"`
		CachedContentTokenCount int `json:"cachedContentTokenCount"`
	} `json:"usageMetadata"`
}

func parseOpenAIDelta(data []byte) string {
	var chunk OpenAIChunk
	if err := json.Unmarshal(data, &chunk); err != nil {
		return ""
	}

	for _, choice := range chunk.Choices {
		if choice.Delta.Content != "" {
			return choice.Delta.Content
		}
	}

	return ""
}

func parseOpenAIFullDelta(data []byte) (string, string) {
	var response OpenAIResponse
	if err := json.Unmarshal(data, &response); err != nil {
		return "", ""
	}

	for _, choice := range response.Choices {
		if choice.Message.Content != "" || choice.Message.ReasoningContent != "" {
			return choice.Message.Content, choice.Message.ReasoningContent
		}
	}

	return "", ""
}

func parseOpenAIUsage(data []byte) (UsageRecord, bool) {
	var chunk OpenAIChunk
	if err := json.Unmarshal(data, &chunk); err == nil && chunk.Usage != nil {
		rec := UsageRecord{
			PromptTokens:     chunk.Usage.PromptTokens,
			CompletionTokens: chunk.Usage.CompletionTokens,
			TotalTokens:      chunk.Usage.TotalTokens,
		}
		if chunk.Usage.PromptTokensDetails != nil {
			rec.CacheReadTokens = chunk.Usage.PromptTokensDetails.CachedTokens
		}
		return rec, true
	}

	var response OpenAIResponse
	if err := json.Unmarshal(data, &response); err != nil || response.Usage == nil {
		return UsageRecord{}, false
	}

	rec := UsageRecord{
		PromptTokens:     response.Usage.PromptTokens,
		CompletionTokens: response.Usage.CompletionTokens,
		TotalTokens:      response.Usage.TotalTokens,
	}
	if response.Usage.PromptTokensDetails != nil {
		rec.CacheReadTokens = response.Usage.PromptTokensDetails.CachedTokens
	}
	return rec, true
}

func parseGeminiUsage(data []byte) (UsageRecord, bool) {
	var chunk GeminiChunk
	if err := json.Unmarshal(data, &chunk); err == nil && chunk.UsageMetadata != nil {
		return UsageRecord{
			PromptTokens:     chunk.UsageMetadata.PromptTokenCount,
			CompletionTokens: chunk.UsageMetadata.CandidatesTokenCount,
			TotalTokens:      chunk.UsageMetadata.TotalTokenCount,
			CacheReadTokens:  chunk.UsageMetadata.CachedContentTokenCount,
		}, true
	}

	var response GeminiResponse
	if err := json.Unmarshal(data, &response); err != nil || response.UsageMetadata == nil {
		return UsageRecord{}, false
	}

	return UsageRecord{
		PromptTokens:     response.UsageMetadata.PromptTokenCount,
		CompletionTokens: response.UsageMetadata.CandidatesTokenCount,
		TotalTokens:      response.UsageMetadata.TotalTokenCount,
		CacheReadTokens:  response.UsageMetadata.CachedContentTokenCount,
	}, true
}

func parseGeminiDelta(data []byte) string {
	var chunk GeminiChunk
	if err := json.Unmarshal(data, &chunk); err != nil {
		return ""
	}

	for _, candidate := range chunk.Candidates {
		for _, part := range candidate.Content.Parts {
			if part.Text != "" {
				return part.Text
			}
		}
	}

	return ""
}

// AnthropicResponse covers both streaming delta events and full responses.
// Streaming: {"type":"content_block_delta","delta":{"type":"text_delta","text":"..."}}
//            {"type":"content_block_delta","delta":{"type":"thinking_delta","thinking":"..."}}
// Full:      {"content":[{"type":"text","text":"..."}],"usage":{"input_tokens":N,"output_tokens":M}}
type AnthropicStreamEvent struct {
	Type  string `json:"type"`
	Delta *struct {
		Type     string `json:"type"`
		Text     string `json:"text"`
		Thinking string `json:"thinking"`
	} `json:"delta"`
}

type AnthropicResponse struct {
	Content []struct {
		Type    string `json:"type"`
		Text    string `json:"text"`
		Thinking string `json:"thinking"`
	} `json:"content"`
	Usage *struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
	} `json:"usage"`
}

func parseAnthropicDelta(data []byte) string {
	var event AnthropicStreamEvent
	if err := json.Unmarshal(data, &event); err != nil {
		return ""
	}
	if event.Type == "content_block_delta" && event.Delta != nil && event.Delta.Type == "text_delta" {
		return event.Delta.Text
	}
	return ""
}

func parseAnthropicFullDelta(data []byte) (string, string) {
	var resp AnthropicResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		return "", ""
	}
	var text, thinking strings.Builder
	for _, block := range resp.Content {
		switch block.Type {
		case "thinking":
			thinking.WriteString(block.Thinking)
		case "text":
			text.WriteString(block.Text)
		}
	}
	return text.String(), thinking.String()
}

func parseAnthropicUsage(data []byte) (UsageRecord, bool) {
	var resp AnthropicResponse
	if err := json.Unmarshal(data, &resp); err != nil || resp.Usage == nil {
		return UsageRecord{}, false
	}
	return UsageRecord{
		PromptTokens:     resp.Usage.InputTokens,
		CompletionTokens: resp.Usage.OutputTokens,
		TotalTokens:      resp.Usage.InputTokens + resp.Usage.OutputTokens,
	}, true
}

func parseGeminiFullDelta(data []byte) (string, string) {
	var response GeminiResponse
	if err := json.Unmarshal(data, &response); err != nil {
		return "", ""
	}

	var text, thinking strings.Builder
	for _, candidate := range response.Candidates {
		for _, part := range candidate.Content.Parts {
			if part.Thought {
				thinking.WriteString(part.Text)
			} else {
				text.WriteString(part.Text)
			}
		}
	}

	return text.String(), thinking.String()
}
