// Package config loads and validates runtime configuration from environment variables.
package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/joho/godotenv"
)

// Config holds all runtime configuration for Symbiont.
type Config struct {
	Server   ServerConfig
	Database DatabaseConfig
	Auth     AuthConfig
	Providers ProviderConfig
	Budgets  BudgetDefaults
	Storage  StorageConfig
	Otel     OtelConfig
	Log      LogConfig
}

type ServerConfig struct {
	Host string
	Port int
	Env  string // "development" | "production"
}

type DatabaseConfig struct {
	URL             string
	MaxOpenConns    int
	MaxIdleConns    int
	ConnMaxLifetime time.Duration
}

type AuthConfig struct {
	JWTSecret string
}

type ProviderConfig struct {
	DefaultProvider string
	AnthropicAPIKey string
	OpenAIAPIKey    string
	OpenRouterAPIKey string
	OllamaBaseURL   string
}

type BudgetDefaults struct {
	AgentBudgetCents   int64
	ProjectBudgetCents int64
}

type StorageConfig struct {
	Backend   string // "local" | "s3"
	LocalPath string
	S3Bucket  string
	S3Region  string
}

type OtelConfig struct {
	Enabled          bool
	ExporterEndpoint string
}

type LogConfig struct {
	Level  string // "debug" | "info" | "warn" | "error"
	Format string // "pretty" | "json"
}

// Load reads configuration from environment variables.
// It will load a .env file from the working directory if present.
func Load() (*Config, error) {
	// Best-effort .env load (ignored in production)
	_ = godotenv.Load()

	cfg := &Config{
		Server: ServerConfig{
			Host: getEnvOrDefault("SERVER_HOST", "0.0.0.0"),
			Port: getEnvIntOrDefault("SERVER_PORT", 8080),
			Env:  getEnvOrDefault("SERVER_ENV", "development"),
		},
		Database: DatabaseConfig{
			URL:             mustEnv("DATABASE_URL"),
			MaxOpenConns:    getEnvIntOrDefault("DB_MAX_OPEN_CONNS", 25),
			MaxIdleConns:    getEnvIntOrDefault("DB_MAX_IDLE_CONNS", 5),
			ConnMaxLifetime: getEnvDurationOrDefault("DB_CONN_MAX_LIFETIME", 5*time.Minute),
		},
		Auth: AuthConfig{
			JWTSecret: mustEnv("JWT_SECRET"),
		},
		Providers: ProviderConfig{
			DefaultProvider:  getEnvOrDefault("DEFAULT_PROVIDER", "anthropic"),
			AnthropicAPIKey:  os.Getenv("ANTHROPIC_API_KEY"),
			OpenAIAPIKey:     os.Getenv("OPENAI_API_KEY"),
			OpenRouterAPIKey: os.Getenv("OPENROUTER_API_KEY"),
			OllamaBaseURL:    getEnvOrDefault("OLLAMA_BASE_URL", "http://localhost:11434"),
		},
		Budgets: BudgetDefaults{
			AgentBudgetCents:   getEnvInt64OrDefault("DEFAULT_AGENT_BUDGET_USD", 500),
			ProjectBudgetCents: getEnvInt64OrDefault("DEFAULT_PROJECT_BUDGET_USD", 10000),
		},
		Storage: StorageConfig{
			Backend:   getEnvOrDefault("STORAGE_BACKEND", "local"),
			LocalPath: getEnvOrDefault("STORAGE_LOCAL_PATH", "./data/blobs"),
			S3Bucket:  os.Getenv("STORAGE_S3_BUCKET"),
			S3Region:  os.Getenv("STORAGE_S3_REGION"),
		},
		Otel: OtelConfig{
			Enabled:          getEnvBoolOrDefault("OTEL_ENABLED", false),
			ExporterEndpoint: getEnvOrDefault("OTEL_EXPORTER_OTLP_ENDPOINT", "http://localhost:4317"),
		},
		Log: LogConfig{
			Level:  getEnvOrDefault("LOG_LEVEL", "info"),
			Format: getEnvOrDefault("LOG_FORMAT", "pretty"),
		},
	}

	if err := cfg.validate(); err != nil {
		return nil, fmt.Errorf("config validation: %w", err)
	}

	return cfg, nil
}

func (c *Config) validate() error {
	if strings.TrimSpace(c.Auth.JWTSecret) == "" {
		return fmt.Errorf("JWT_SECRET must be set")
	}
	if len(c.Auth.JWTSecret) < 32 {
		return fmt.Errorf("JWT_SECRET must be at least 32 characters")
	}
	if c.Database.URL == "" {
		return fmt.Errorf("DATABASE_URL must be set")
	}
	return nil
}

func (c *Config) Addr() string {
	return fmt.Sprintf("%s:%d", c.Server.Host, c.Server.Port)
}

func (c *Config) IsDevelopment() bool {
	return c.Server.Env == "development"
}

// ── helpers ──────────────────────────────────────────────────

func mustEnv(key string) string {
	v := os.Getenv(key)
	if v == "" {
		// Return empty; caller validates
	}
	return v
}

func getEnvOrDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func getEnvIntOrDefault(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

func getEnvInt64OrDefault(key string, def int64) int64 {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			return n
		}
	}
	return def
}

func getEnvBoolOrDefault(key string, def bool) bool {
	if v := os.Getenv(key); v != "" {
		b, err := strconv.ParseBool(v)
		if err == nil {
			return b
		}
	}
	return def
}

func getEnvDurationOrDefault(key string, def time.Duration) time.Duration {
	if v := os.Getenv(key); v != "" {
		d, err := time.ParseDuration(v)
		if err == nil {
			return d
		}
	}
	return def
}
