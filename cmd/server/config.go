package main

import (
	"os"
	"strconv"
	"time"

	"github.com/joho/godotenv"
)

type Config struct {
	Port              int
	LogDir            string
	DatabasePath      string
	OpenAIBaseURL     string
	OpenAIAPIKey      string
	GeminiBaseURL     string
	GeminiAPIKey      string
	CopilotBaseURL    string
	CopilotAPIKey     string
	AnthropicBaseURL  string
	AnthropicAPIKey   string
	XAIBaseURL        string
	XAIAPIKey         string
	GroqBaseURL       string
	GroqAPIKey        string
	MistralBaseURL    string
	MistralAPIKey     string
	OpenRouterBaseURL string
	OpenRouterAPIKey  string
	TogetherAIBaseURL string
	TogetherAIAPIKey  string
	PerplexityBaseURL string
	PerplexityAPIKey  string
	CerebrasBaseURL   string
	CerebrasAPIKey    string
	DeepInfraBaseURL  string
	DeepInfraAPIKey   string
	RequestTimeout    time.Duration
	RateLimitInterval time.Duration
	Debug             bool
}

func LoadConfig() (Config, error) {
	_ = godotenv.Load()

	cfg := Config{
		Port:              readEnvInt("PORT", 8888),
		LogDir:            readEnvString("LOG_DIR", "./logs"),
		DatabasePath:      readEnvString("DATABASE_PATH", "./data/opencode-proxy.db"),
		OpenAIBaseURL:     readEnvString("OPENAI_BASE_URL", "https://api.openai.com"),
		OpenAIAPIKey:      readEnvString("OPENAI_API_KEY", ""),
		GeminiBaseURL:     readEnvString("GEMINI_BASE_URL", "https://generativelanguage.googleapis.com"),
		GeminiAPIKey:      readEnvString("GEMINI_API_KEY", ""),
		CopilotBaseURL:    readEnvString("COPILOT_BASE_URL", "https://api.githubcopilot.com"),
		CopilotAPIKey:     readEnvString("COPILOT_API_KEY", ""),
		AnthropicBaseURL:  readEnvString("ANTHROPIC_BASE_URL", "https://api.anthropic.com/v1"),
		AnthropicAPIKey:   readEnvString("ANTHROPIC_API_KEY", ""),
		XAIBaseURL:        readEnvString("XAI_BASE_URL", "https://api.x.ai/v1"),
		XAIAPIKey:         readEnvString("XAI_API_KEY", ""),
		GroqBaseURL:       readEnvString("GROQ_BASE_URL", "https://api.groq.com/openai/v1"),
		GroqAPIKey:        readEnvString("GROQ_API_KEY", ""),
		MistralBaseURL:    readEnvString("MISTRAL_BASE_URL", "https://api.mistral.ai/v1"),
		MistralAPIKey:     readEnvString("MISTRAL_API_KEY", ""),
		OpenRouterBaseURL: readEnvString("OPENROUTER_BASE_URL", "https://openrouter.ai/api/v1"),
		OpenRouterAPIKey:  readEnvString("OPENROUTER_API_KEY", ""),
		TogetherAIBaseURL: readEnvString("TOGETHERAI_BASE_URL", "https://api.together.xyz/v1"),
		TogetherAIAPIKey:  readEnvString("TOGETHERAI_API_KEY", ""),
		PerplexityBaseURL: readEnvString("PERPLEXITY_BASE_URL", "https://api.perplexity.ai"),
		PerplexityAPIKey:  readEnvString("PERPLEXITY_API_KEY", ""),
		CerebrasBaseURL:   readEnvString("CEREBRAS_BASE_URL", "https://api.cerebras.ai/v1"),
		CerebrasAPIKey:    readEnvString("CEREBRAS_API_KEY", ""),
		DeepInfraBaseURL:  readEnvString("DEEPINFRA_BASE_URL", "https://api.deepinfra.com/v1/openai"),
		DeepInfraAPIKey:   readEnvString("DEEPINFRA_API_KEY", ""),
		RequestTimeout:    readEnvDuration("REQUEST_TIMEOUT", 5*time.Minute),
		RateLimitInterval: readEnvDuration("RATE_LIMIT_INTERVAL", 0),
		Debug:             readEnvBool("DEBUG", false),
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
	case "openai", "gemini", "copilot", "anthropic", "xai", "groq", "mistral", "openrouter", "togetherai", "perplexity", "cerebras", "deepinfra":
		return true
	default:
		return false
	}
}
