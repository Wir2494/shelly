package main

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"personal_ai/internal/api"
)

func TestAgentExecutorDynamicCommands(t *testing.T) {
	base := t.TempDir()
	cfg := &AgentConfig{
		Execution: AgentExecConfig{
			DefaultTimeoutSec: 2,
			MaxOutputKB:       8,
			BaseDir:           base,
			DynamicAllowlist:  []string{"touch", "mkdir", "count", "find"},
		},
	}
	exec := newAgentExecutor(cfg)

	resp := exec.Execute(context.Background(), api.CommandRequest{Command: "mkdir", Args: []string{"Movies"}, ChatID: 1})
	if !resp.Ok {
		t.Fatalf("mkdir failed: %+v", resp)
	}

	_ = exec.Execute(context.Background(), api.CommandRequest{Command: "touch", Args: []string{"Movies/a.mp4"}, ChatID: 1})
	_ = exec.Execute(context.Background(), api.CommandRequest{Command: "touch", Args: []string{"Movies/b.mp4"}, ChatID: 1})

	resp = exec.Execute(context.Background(), api.CommandRequest{Command: "count", Args: []string{"Movies"}, ChatID: 1})
	if !resp.Ok {
		t.Fatalf("count failed: %+v", resp)
	}
	if strings.TrimSpace(resp.Stdout) != "2" {
		t.Fatalf("expected count 2, got %q", resp.Stdout)
	}

	resp = exec.Execute(context.Background(), api.CommandRequest{Command: "find", Args: []string{"Movies"}, ChatID: 1})
	if !resp.Ok {
		t.Fatalf("find failed: %+v", resp)
	}
	if !strings.Contains(resp.Stdout, filepath.Join(base, "Movies")) {
		t.Fatalf("expected find result to include Movies dir")
	}
}
