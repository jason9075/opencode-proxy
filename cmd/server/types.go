package main

import "time"

type RequestRecord struct {
	ID          string
	SessionID   string
	Provider    string
	Model       string
	Stream      bool
	Path        string
	User        string
	Temperature *float64
	TopP        *float64
	MaxTokens   *int
	CreatedAt   time.Time
}

type MessageRecord struct {
	RequestID string
	Role      string
	Content   string
	Name      string
	CreatedAt time.Time
}

type ResponseDelta struct {
	RequestID string
	Seq       int
	Delta     string
	CreatedAt time.Time
}

type UsageRecord struct {
	RequestID        string
	PromptTokens     int
	CompletionTokens int
	TotalTokens      int
}

type LoggedMessage struct {
	Role    string
	Content string
	Name    string
}

type ParsedRequest struct {
	SessionID   string
	Model       string
	Stream      bool
	User        string
	Temperature *float64
	TopP        *float64
	MaxTokens   *int
	Messages    []LoggedMessage
	UserTurns   []string
}
