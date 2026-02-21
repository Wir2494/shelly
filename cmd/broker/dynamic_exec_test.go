package main

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"personal_ai/internal/api"
)

func TestLocalExecutorTouchMkdirCountFind(t *testing.T) {
	base := t.TempDir()
	cfg := &BrokerConfig{
		Execution: ExecutionConfig{
			Mode: "local",
			Local: LocalExecutionConfig{
				DefaultTimeoutSec: 2,
				MaxOutputKB:       8,
				BaseDir:           base,
				DynamicAllowlist:  []string{"touch", "mkdir", "count", "find"},
			},
		},
	}
	exec := newLocalExecutor(cfg)

	// mkdir Movies
	resp, err := exec.Execute(context.Background(), api.CommandRequest{Command: "mkdir", Args: []string{"Movies"}, ChatID: 1})
	if err != nil || !resp.Ok {
		t.Fatalf("mkdir failed: %+v err=%v", resp, err)
	}

	// touch two files
	_, _ = exec.Execute(context.Background(), api.CommandRequest{Command: "touch", Args: []string{"Movies/a.mp4"}, ChatID: 1})
	_, _ = exec.Execute(context.Background(), api.CommandRequest{Command: "touch", Args: []string{"Movies/b.mp4"}, ChatID: 1})

	// count files
	resp, err = exec.Execute(context.Background(), api.CommandRequest{Command: "count", Args: []string{"Movies"}, ChatID: 1})
	if err != nil || !resp.Ok {
		t.Fatalf("count failed: %+v err=%v", resp, err)
	}
	if strings.TrimSpace(resp.Stdout) != "2" {
		t.Fatalf("expected count 2, got %q", resp.Stdout)
	}

	// find Movies
	resp, err = exec.Execute(context.Background(), api.CommandRequest{Command: "find", Args: []string{"Movies"}, ChatID: 1})
	if err != nil || !resp.Ok {
		t.Fatalf("find failed: %+v err=%v", resp, err)
	}
	if !strings.Contains(resp.Stdout, filepath.Join(base, "Movies")) {
		t.Fatalf("expected find result to include Movies dir")
	}
}

func TestPingValidationRejectsBadHost(t *testing.T) {
	base := t.TempDir()
	cfg := &BrokerConfig{
		Execution: ExecutionConfig{
			Mode: "local",
			Local: LocalExecutionConfig{
				DefaultTimeoutSec: 2,
				MaxOutputKB:       8,
				BaseDir:           base,
				DynamicAllowlist:  []string{"ping"},
			},
		},
	}
	exec := newLocalExecutor(cfg)

	resp, err := exec.Execute(context.Background(), api.CommandRequest{Command: "ping", Args: []string{"bad host"}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Ok {
		t.Fatalf("expected ping to fail for invalid host")
	}
}
