package main

import (
	"os"
	"strconv"
	"time"

	"github.com/joho/godotenv"
)

type Config struct {
	Port           int
	LogDir         string
	DatabasePath   string
	OpenAIBaseURL  string
	OpenAIAPIKey   string
	GeminiBaseURL  string
	GeminiAPIKey   string
	CopilotBaseURL string
	CopilotAPIKey  string
	RequestTimeout time.Duration
	Debug          bool
}

func LoadConfig() (Config, error) {
	_ = godotenv.Load()

	cfg := Config{
		Port:           readEnvInt("PORT", 8888),
		LogDir:         readEnvString("LOG_DIR", "./logs"),
		DatabasePath:   readEnvString("DATABASE_PATH", "./data/opencode-proxy.db"),
		OpenAIBaseURL:  readEnvString("OPENAI_BASE_URL", "https://api.openai.com"),
		OpenAIAPIKey:   readEnvString("OPENAI_API_KEY", ""),
		GeminiBaseURL:  readEnvString("GEMINI_BASE_URL", "https://generativelanguage.googleapis.com"),
		GeminiAPIKey:   readEnvString("GEMINI_API_KEY", ""),
		CopilotBaseURL: readEnvString("COPILOT_BASE_URL", "https://api.githubcopilot.com"),
		CopilotAPIKey:  readEnvString("COPILOT_API_KEY", ""),
		RequestTimeout: readEnvDuration("REQUEST_TIMEOUT", 5*time.Minute),
		Debug:          readEnvBool("DEBUG", false),
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

func readEnvBool(key string, fallback bool) bool {
	value := os.Getenv(key)
	if value == "" {
		return fallback
	}
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return fallback
	}
	return parsed
}

func validProvider(provider string) bool {
	switch provider {
	case "openai", "gemini", "copilot":
		return true
	default:
		return false
	}
}
