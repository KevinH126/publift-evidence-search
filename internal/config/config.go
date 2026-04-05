package config

import (
	"os"
	"strconv"
)

// Config holds all configuration loaded from environment variables.
type Config struct {
	Port              string
	DatabaseURL       string
	RedisURL          string
	EmbedderURL       string
	WorkerConcurrency int
}

// Load reads configuration from environment with sensible defaults.
func Load() *Config {
	return &Config{
		Port:              getEnv("PORT", "8080"),
		DatabaseURL:       getEnv("DATABASE_URL", "postgres://semantic:semantic@localhost:5432/semantic?sslmode=disable"),
		RedisURL:          getEnv("REDIS_URL", "redis://localhost:6379/0"),
		EmbedderURL:       getEnv("EMBEDDER_URL", "http://localhost:8000"),
		WorkerConcurrency: getEnvInt("WORKER_CONCURRENCY", 4),
	}
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func getEnvInt(key string, fallback int) int {
	if v := os.Getenv(key); v != "" {
		if i, err := strconv.Atoi(v); err == nil {
			return i
		}
	}
	return fallback
}
