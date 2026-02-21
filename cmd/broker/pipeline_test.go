package main

import (
	"context"
	"strings"
	"testing"
	"time"

	"personal_ai/internal/api"
)

type senderStub struct {
	calls []string
}

func (s *senderStub) send(_ string, _ int64, text string) error {
	s.calls = append(s.calls, text)
	return nil
}

type executorStub func(req api.CommandRequest) (*api.CommandResponse, error)

func (e executorStub) Execute(ctx context.Context, req api.CommandRequest) (*api.CommandResponse, error) {
	return e(req)
}

func TestPipelineUnauthorizedStopsBeforeExecute(t *testing.T) {
	cfg := &BrokerConfig{
		Telegram: TelegramConfig{
			BotToken:       "token",
			AllowedUserIDs: []int64{1},
		},
	}
	rl := newRateLimiter(time.Minute, 0)
	called := false
	exec := executorStub(func(req api.CommandRequest) (*api.CommandResponse, error) {
		called = true
		return &api.CommandResponse{Ok: true, ExitCode: 0}, nil
	})
	sender := &senderStub{}

	update := TelegramUpdate{Message: &TelegramMessage{
		From: TelegramUser{ID: 2},
		Chat: TelegramChat{ID: 99},
		Text: "status",
	}}

	processUpdateWithSender(cfg, rl, exec, update, sender.send)

	if called {
		t.Fatalf("expected executor not to be called")
	}
	if len(sender.calls) != 1 {
		t.Fatalf("expected 1 send call, got %d", len(sender.calls))
	}
	if sender.calls[0] != "Unauthorized user." {
		t.Fatalf("unexpected response: %q", sender.calls[0])
	}
}

func TestPipelineHelpSendsAllowlist(t *testing.T) {
	cfg := &BrokerConfig{
		Telegram: TelegramConfig{
			BotToken:       "token",
			AllowedUserIDs: []int64{1},
		},
		Policy: PolicyConfig{
			CommandAllowlist: []string{"status", "disk"},
		},
	}
	rl := newRateLimiter(time.Minute, 0)
	called := false
	exec := executorStub(func(req api.CommandRequest) (*api.CommandResponse, error) {
		called = true
		return &api.CommandResponse{Ok: true, ExitCode: 0}, nil
	})
	sender := &senderStub{}

	update := TelegramUpdate{Message: &TelegramMessage{
		From: TelegramUser{ID: 1},
		Chat: TelegramChat{ID: 99},
		Text: "/help",
	}}

	processUpdateWithSender(cfg, rl, exec, update, sender.send)

	if called {
		t.Fatalf("expected executor not to be called")
	}
	if len(sender.calls) != 1 {
		t.Fatalf("expected 1 send call, got %d", len(sender.calls))
	}
	expected := "Allowed commands: " + strings.Join(cfg.Policy.CommandAllowlist, ", ")
	if sender.calls[0] != expected {
		t.Fatalf("unexpected response: %q", sender.calls[0])
	}
}
