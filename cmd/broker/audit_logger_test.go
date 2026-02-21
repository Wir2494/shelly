package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestAuditLoggerWritesFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.log")
	logger := newAuditLogger(AuditConfig{FilePath: path})
	if logger == nil {
		t.Fatalf("expected logger")
	}

	logger.Log(AuditEvent{
		Timestamp: time.Date(2025, 1, 2, 3, 4, 5, 0, time.UTC),
		Type:      "execution",
		UserID:    1,
		ChatID:    2,
		Command:   "status",
		Outcome:   "ok",
		Message:   "done",
	})

	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !strings.Contains(string(b), "execution") {
		t.Fatalf("expected log line, got: %s", string(b))
	}
}
