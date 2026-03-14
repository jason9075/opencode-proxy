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
		PromptTokensDetails *struct {
			CachedTokens int `json:"cached_tokens"`
		} `json:"prompt_tokens_details"`
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
			Content string `json:"content"`
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
				Text string `json:"text"`
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
