package main

import "encoding/json"

type OpenAIChunk struct {
	Choices []struct {
		Delta struct {
			Content string `json:"content"`
		} `json:"delta"`
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
	Usage *struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
		TotalTokens      int `json:"total_tokens"`
	} `json:"usage"`
}

type GeminiChunk struct {
	Candidates []struct {
		Content struct {
			Parts []struct {
				Text string `json:"text"`
			} `json:"parts"`
		} `json:"content"`
	} `json:"candidates"`
}

type OpenAIResponse struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
	Usage *struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
		TotalTokens      int `json:"total_tokens"`
	} `json:"usage"`
}

type GeminiResponse struct {
	Candidates []struct {
		Content struct {
			Parts []struct {
				Text string `json:"text"`
			} `json:"parts"`
		} `json:"content"`
	} `json:"candidates"`
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

func parseOpenAIFullDelta(data []byte) string {
	var response OpenAIResponse
	if err := json.Unmarshal(data, &response); err != nil {
		return ""
	}

	for _, choice := range response.Choices {
		if choice.Message.Content != "" {
			return choice.Message.Content
		}
	}

	return ""
}

func parseOpenAIUsage(data []byte) (UsageRecord, bool) {
	var chunk OpenAIChunk
	if err := json.Unmarshal(data, &chunk); err == nil && chunk.Usage != nil {
		return UsageRecord{
			PromptTokens:     chunk.Usage.PromptTokens,
			CompletionTokens: chunk.Usage.CompletionTokens,
			TotalTokens:      chunk.Usage.TotalTokens,
		}, true
	}

	var response OpenAIResponse
	if err := json.Unmarshal(data, &response); err != nil || response.Usage == nil {
		return UsageRecord{}, false
	}

	return UsageRecord{
		PromptTokens:     response.Usage.PromptTokens,
		CompletionTokens: response.Usage.CompletionTokens,
		TotalTokens:      response.Usage.TotalTokens,
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

func parseGeminiFullDelta(data []byte) string {
	var response GeminiResponse
	if err := json.Unmarshal(data, &response); err != nil {
		return ""
	}

	for _, candidate := range response.Candidates {
		for _, part := range candidate.Content.Parts {
			if part.Text != "" {
				return part.Text
			}
		}
	}

	return ""
}
