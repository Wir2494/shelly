package main

import (
	"context"
	"strings"
	"testing"

	"personal_ai/internal/api"
)

func TestLocalExecutorRunsAllowlistedCommand(t *testing.T) {
	cfg := &BrokerConfig{
		Execution: ExecutionConfig{
			Mode: "local",
			Local: LocalExecutionConfig{
				DefaultTimeoutSec: 2,
				MaxOutputKB:       8,
				CommandAllowlist: map[string]api.AllowedCommand{
					"echo": {Exec: "/bin/echo", Args: []string{"hello"}},
				},
			},
		},
	}

	exec := newLocalExecutor(cfg)
	resp, err := exec.Execute(context.Background(), api.CommandRequest{Command: "echo"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !resp.Ok {
		t.Fatalf("expected ok response, got: %+v", resp)
	}
	if got := strings.TrimSpace(resp.Stdout); got != "hello" {
		t.Fatalf("expected stdout 'hello', got %q", got)
	}
}

func TestLocalExecutorDynamicPwd(t *testing.T) {
	base := t.TempDir()
	cfg := &BrokerConfig{
		Execution: ExecutionConfig{
			Mode: "local",
			Local: LocalExecutionConfig{
				DefaultTimeoutSec: 2,
				MaxOutputKB:       8,
				BaseDir:           base,
				DynamicAllowlist:  []string{"pwd"},
			},
		},
	}

	exec := newLocalExecutor(cfg)
	resp, err := exec.Execute(context.Background(), api.CommandRequest{Command: "pwd", ChatID: 42})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !resp.Ok {
		t.Fatalf("expected ok response, got: %+v", resp)
	}
	if got := strings.TrimSpace(resp.Stdout); got != base {
		t.Fatalf("expected stdout %q, got %q", base, got)
	}
}
