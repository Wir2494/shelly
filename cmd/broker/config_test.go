package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"personal_ai/internal/api"
)

func TestLoadConfigDefaultsAndAllowlistDerivation(t *testing.T) {
	cfgIn := BrokerConfig{
		Telegram: TelegramConfig{},
		Execution: ExecutionConfig{
			Mode: "local",
			Local: LocalExecutionConfig{
				CommandAllowlist: map[string]api.AllowedCommand{
					"status": {Exec: "/bin/echo", Args: []string{"ok"}},
				},
				DynamicAllowlist: []string{"pwd"},
			},
		},
		Policy: PolicyConfig{},
	}

	b, err := json.Marshal(cfgIn)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	path := filepath.Join(t.TempDir(), "broker.json")
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
	if cfg.Telegram.Mode != "polling" {
		t.Fatalf("expected default telegram.mode polling, got %q", cfg.Telegram.Mode)
	}
	if cfg.Telegram.PollIntervalSec != 3 {
		t.Fatalf("expected default poll_interval_sec 3, got %d", cfg.Telegram.PollIntervalSec)
	}
	if cfg.Execution.Local.DefaultTimeoutSec != 10 {
		t.Fatalf("expected default local timeout 10, got %d", cfg.Execution.Local.DefaultTimeoutSec)
	}
	if cfg.Execution.Local.MaxOutputKB != 8 {
		t.Fatalf("expected default local max output 8, got %d", cfg.Execution.Local.MaxOutputKB)
	}
	if len(cfg.Policy.CommandAllowlist) == 0 {
		t.Fatalf("expected derived command_allowlist")
	}
}
