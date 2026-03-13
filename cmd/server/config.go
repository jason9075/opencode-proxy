package main

import (
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/joho/godotenv"
)

type Config struct {
	Port           int
	Provider       string
	LogDir         string
	DatabasePath   string
	OpenAIBaseURL  string
	OpenAIAPIKey   string
	GeminiBaseURL  string
	GeminiAPIKey   string
	CopilotBaseURL string
	CopilotAPIKey  string
	RequestTimeout time.Duration
}

func LoadConfig() (Config, error) {
	_ = godotenv.Load()

	cfg := Config{
		Port:           readEnvInt("PORT", 8888),
		Provider:       readEnvString("PROVIDER", "openai"),
		LogDir:         readEnvString("LOG_DIR", "./logs"),
		DatabasePath:   readEnvString("DATABASE_PATH", "./data/opencode-proxy.db"),
		OpenAIBaseURL:  readEnvString("OPENAI_BASE_URL", "https://api.openai.com"),
		OpenAIAPIKey:   readEnvString("OPENAI_API_KEY", ""),
		GeminiBaseURL:  readEnvString("GEMINI_BASE_URL", "https://generativelanguage.googleapis.com"),
		GeminiAPIKey:   readEnvString("GEMINI_API_KEY", ""),
		CopilotBaseURL: readEnvString("COPILOT_BASE_URL", "https://api.githubcopilot.com"),
		CopilotAPIKey:  readEnvString("COPILOT_API_KEY", ""),
		RequestTimeout: readEnvDuration("REQUEST_TIMEOUT", 5*time.Minute),
	}

	if cfg.Provider != "openai" && cfg.Provider != "gemini" && cfg.Provider != "copilot" {
		return Config{}, fmt.Errorf("PROVIDER must be openai, gemini, or copilot")
	}

	if cfg.Provider == "openai" && cfg.OpenAIAPIKey == "" {
		return Config{}, fmt.Errorf("OPENAI_API_KEY is required")
	}

	if cfg.Provider == "gemini" && cfg.GeminiAPIKey == "" {
		return Config{}, fmt.Errorf("GEMINI_API_KEY is required")
	}

	if cfg.Provider == "copilot" && cfg.CopilotAPIKey == "" {
		return Config{}, fmt.Errorf("COPILOT_API_KEY is required")
	}

	return cfg, nil
}

func readEnvString(key string, fallback string) string {
	value := os.Getenv(key)
	if value == "" {
		return fallback
	}
	return value
}

func readEnvInt(key string, fallback int) int {
	value := os.Getenv(key)
	if value == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return fallback
	}
	return parsed
}

func readEnvDuration(key string, fallback time.Duration) time.Duration {
	value := os.Getenv(key)
	if value == "" {
		return fallback
	}
	parsed, err := time.ParseDuration(value)
	if err != nil {
		return fallback
	}
	return parsed
}
