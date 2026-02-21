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

func (s *senderStub) Send(_ int64, text string) error {
	s.calls = append(s.calls, text)
	return nil
}

type executorStub func(req api.CommandRequest) (*api.CommandResponse, error)

func (e executorStub) Execute(ctx context.Context, req api.CommandRequest) (*api.CommandResponse, error) {
	return e(req)
}

type llmStub struct {
	decision *api.LLMDecision
	err      error
	calls    int
}

func (l *llmStub) Map(ctx context.Context, userText string, allowlist []string) (*api.LLMDecision, error) {
	l.calls++
	return l.decision, l.err
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
	broker := newBroker(cfg, rl, exec, sender, nil)

	update := TelegramUpdate{Message: &TelegramMessage{
		From: TelegramUser{ID: 2},
		Chat: TelegramChat{ID: 99},
		Text: "status",
	}}

	broker.processUpdate(update)

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
	broker := newBroker(cfg, rl, exec, sender, nil)

	update := TelegramUpdate{Message: &TelegramMessage{
		From: TelegramUser{ID: 1},
		Chat: TelegramChat{ID: 99},
		Text: "/help",
	}}

	broker.processUpdate(update)

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

func TestPipelineLLMChatSkipsExecution(t *testing.T) {
	cfg := &BrokerConfig{
		Telegram: TelegramConfig{
			BotToken:       "token",
			AllowedUserIDs: []int64{1},
		},
		LLM: LLMConfig{
			Enabled: true,
		},
		Policy: PolicyConfig{
			CommandAllowlist: []string{"status"},
		},
	}
	rl := newRateLimiter(time.Minute, 0)
	called := false
	exec := executorStub(func(req api.CommandRequest) (*api.CommandResponse, error) {
		called = true
		return &api.CommandResponse{Ok: true, ExitCode: 0}, nil
	})
	sender := &senderStub{}
	llm := &llmStub{decision: &api.LLMDecision{Type: "chat", Response: "hello", Confidence: 1}}
	broker := newBroker(cfg, rl, exec, sender, llm)

	update := TelegramUpdate{Message: &TelegramMessage{
		From: TelegramUser{ID: 1},
		Chat: TelegramChat{ID: 99},
		Text: "hi",
	}}

	broker.processUpdate(update)

	if called {
		t.Fatalf("expected executor not to be called")
	}
	if llm.calls != 1 {
		t.Fatalf("expected llm to be called once, got %d", llm.calls)
	}
	if len(sender.calls) != 1 || sender.calls[0] != "hello" {
		t.Fatalf("unexpected response: %v", sender.calls)
	}
}
