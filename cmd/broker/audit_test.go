package main

import (
	"strings"
	"testing"
	"time"
)

func TestFormatAuditLine(t *testing.T) {
	e := AuditEvent{
		Timestamp: time.Date(2025, 1, 2, 3, 4, 5, 0, time.UTC),
		Type:      "execution",
		UserID:    1,
		ChatID:    2,
		Command:   "status",
		Outcome:   "ok",
		Message:   "done",
	}

	line := formatAuditLine(e)
	if !strings.Contains(line, "2025-01-02T03:04:05Z") {
		t.Fatalf("missing timestamp: %s", line)
	}
	if !strings.Contains(line, "execution") {
		t.Fatalf("missing type: %s", line)
	}
	if !strings.Contains(line, "user=1") || !strings.Contains(line, "chat=2") {
		t.Fatalf("missing ids: %s", line)
	}
	if !strings.Contains(line, "cmd=\"status\"") {
		t.Fatalf("missing cmd: %s", line)
	}
	if !strings.Contains(line, "outcome=\"ok\"") {
		t.Fatalf("missing outcome: %s", line)
	}
	if !strings.Contains(line, "msg=\"done\"") {
		t.Fatalf("missing msg: %s", line)
	}
}
