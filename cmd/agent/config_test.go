package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestLoadConfigDefaults(t *testing.T) {
	cfgIn := AgentConfig{}
	b, err := json.Marshal(cfgIn)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	path := filepath.Join(t.TempDir(), "agent.json")
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	cfg, err := loadConfig(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.ListenAddr == "" {
		t.Fatalf("expected default listen_addr")
	}
	if cfg.Execution.DefaultTimeoutSec != 10 {
		t.Fatalf("expected default timeout 10, got %d", cfg.Execution.DefaultTimeoutSec)
	}
	if cfg.Execution.MaxOutputKB != 8 {
		t.Fatalf("expected default max output 8, got %d", cfg.Execution.MaxOutputKB)
	}
}
