package main

import (
	"context"
	"strings"
	"testing"

	"personal_ai/internal/api"
)

func TestAgentExecutorBlocksCommand(t *testing.T) {
	cfg := &AgentConfig{
		Execution: AgentExecConfig{
			CommandBlocklist: []string{"status"},
		},
	}
	exec := newAgentExecutor(cfg)

	resp := exec.Execute(context.Background(), api.CommandRequest{Command: "status"})
	if resp.Ok {
		t.Fatalf("expected blocked command to fail")
	}
	if resp.Error != "command blocked" {
		t.Fatalf("unexpected error: %q", resp.Error)
	}
}

func TestAgentExecutorRunsAllowlistedCommand(t *testing.T) {
	cfg := &AgentConfig{
		Execution: AgentExecConfig{
			DefaultTimeoutSec: 2,
			MaxOutputKB:       8,
			CommandAllowlist: map[string]api.AllowedCommand{
				"echo": {Exec: "/bin/echo", Args: []string{"hello"}},
			},
		},
	}
	exec := newAgentExecutor(cfg)

	resp := exec.Execute(context.Background(), api.CommandRequest{Command: "echo"})
	if !resp.Ok {
		t.Fatalf("expected ok response, got: %+v", resp)
	}
	if got := strings.TrimSpace(resp.Stdout); got != "hello" {
		t.Fatalf("expected stdout 'hello', got %q", got)
	}
}

func TestAgentExecutorDynamicPwd(t *testing.T) {
	base := t.TempDir()
	cfg := &AgentConfig{
		Execution: AgentExecConfig{
			DefaultTimeoutSec: 2,
			MaxOutputKB:       8,
			BaseDir:           base,
			DynamicAllowlist:  []string{"pwd"},
		},
	}
	exec := newAgentExecutor(cfg)

	resp := exec.Execute(context.Background(), api.CommandRequest{Command: "pwd", ChatID: 7})
	if !resp.Ok {
		t.Fatalf("expected ok response, got: %+v", resp)
	}
	if got := strings.TrimSpace(resp.Stdout); got != base {
		t.Fatalf("expected stdout %q, got %q", base, got)
	}
}
