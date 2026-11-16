// Package config loads and validates every runtime setting from the environment.
// The application fails fast at startup instead of discovering bad configuration
// on the first request (ADR-0002, ADR-0008).
package config

import (
	"fmt"
	"os"
	"strconv"
	"time"
)

// Config is the validated runtime configuration shared by api/worker/job.
type Config struct {
	Port                  string
	LogLevel              string
	DatabaseURL           string
	JWTSecret             string
	AccessTTL             time.Duration
	RefreshTTL            time.Duration
	ExpectedSchemaVersion string
	MaxConns              int32
	AuditRetentionDays    int
	Timezone              string
}

// Load reads the environment and rejects invalid values. No defaults hide a
// missing production setting; only non-sensitive local conveniences default.
func Load() (Config, error) {
	c := Config{
		Port:                  env("PORT", "8080"),
		LogLevel:              env("LOG_LEVEL", "info"),
		DatabaseURL:           os.Getenv("DATABASE_URL"),
		JWTSecret:             os.Getenv("JWT_SECRET"),
		ExpectedSchemaVersion: os.Getenv("EXPECTED_SCHEMA_VERSION"),
		MaxConns:              int32(envInt("DB_MAX_CONNS", 20, 1, 100)),
		AuditRetentionDays:    envInt("AUDIT_LOG_RETENTION_DAYS", 30, 1, 3650),
		Timezone:              env("APP_TIMEZONE", "Asia/Shanghai"),
	}
	var err error
	if c.AccessTTL, err = duration("ACCESS_TOKEN_TTL", 15*time.Minute); err != nil {
		return c, err
	}
	if c.RefreshTTL, err = duration("REFRESH_TOKEN_TTL", 7*24*time.Hour); err != nil {
		return c, err
	}
	if c.DatabaseURL == "" {
		return c, fmt.Errorf("DATABASE_URL is required")
	}
	if len(c.JWTSecret) < 32 {
		return c, fmt.Errorf("JWT_SECRET must be at least 32 bytes")
	}
	if c.Timezone != "Asia/Shanghai" {
		return c, fmt.Errorf("APP_TIMEZONE is fixed to Asia/Shanghai (ADR-0004)")
	}
	if c.LogLevel != "debug" && c.LogLevel != "info" && c.LogLevel != "warn" && c.LogLevel != "error" {
		return c, fmt.Errorf("LOG_LEVEL must be debug/info/warn/error")
	}
	if c.MaxConns < 1 || c.MaxConns > 50 {
		return c, fmt.Errorf("DB_MAX_CONNS must be 1..50 per instance (ADR-0008 budget)")
	}
	if c.AuditRetentionDays < 1 || c.AuditRetentionDays > 3650 {
		return c, fmt.Errorf("AUDIT_LOG_RETENTION_DAYS must be 1..3650")
	}
	return c, nil
}

func env(k, fallback string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return fallback
}

func envInt(k string, fallback, min, max int) int {
	v := os.Getenv(k)
	if v == "" {
		return fallback
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < min || n > max {
		return -1 // validated by callers via the checks below
	}
	return n
}

func duration(k string, fallback time.Duration) (time.Duration, error) {
	v := os.Getenv(k)
	if v == "" {
		return fallback, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil || d <= 0 {
		return 0, fmt.Errorf("%s must be a positive duration", k)
	}
	return d, nil
}
