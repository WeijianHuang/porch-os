package config

import (
	"strings"
	"testing"
)

func TestLoadRejectsBadRetention(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgresql://u:p@localhost:5432/db?sslmode=disable")
	t.Setenv("JWT_SECRET", strings.Repeat("x", 32))
	t.Setenv("AUDIT_LOG_RETENTION_DAYS", "0")
	if _, err := Load(); err == nil {
		t.Fatal("expected error for retention 0")
	}
}

func TestLoadRejectsForeignTimezone(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgresql://u:p@localhost:5432/db?sslmode=disable")
	t.Setenv("JWT_SECRET", strings.Repeat("x", 32))
	t.Setenv("APP_TIMEZONE", "America/New_York")
	if _, err := Load(); err == nil {
		t.Fatal("expected error for non-Asia/Shanghai timezone")
	}
}
