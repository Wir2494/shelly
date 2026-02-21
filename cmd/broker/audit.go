package main

import (
	"fmt"
	"io"
	"os"
	"sync"
	"time"
)

type auditLogger struct {
	mu     sync.Mutex
	writer io.Writer
}

func newAuditLogger(cfg AuditConfig) AuditLogger {
	if cfg.FilePath == "" {
		return nil
	}
	f, err := os.OpenFile(cfg.FilePath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return nil
	}
	return &auditLogger{writer: f}
}

func (l *auditLogger) Log(event AuditEvent) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.writer == nil {
		return
	}
	line := formatAuditLine(event)
	_, _ = io.WriteString(l.writer, line+"\n")
}

func formatAuditLine(e AuditEvent) string {
	t := e.Timestamp
	if t.IsZero() {
		t = time.Now().UTC()
	}
	msg := e.Message
	if msg == "" {
		msg = "-"
	}
	cmd := e.Command
	if cmd == "" {
		cmd = "-"
	}
	return fmt.Sprintf("%s %s user=%d chat=%d cmd=\"%s\" outcome=\"%s\" msg=\"%s\"",
		t.Format(time.RFC3339), e.Type, e.UserID, e.ChatID, cmd, e.Outcome, msg)
}
