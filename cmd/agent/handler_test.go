package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"personal_ai/internal/api"
)

type execStub struct {
	resp api.CommandResponse
}

func (e execStub) Execute(ctx context.Context, req api.CommandRequest) api.CommandResponse {
	return e.resp
}

func TestCommandHandlerRejectsUnauthorized(t *testing.T) {
	cfg := &AgentConfig{AuthToken: "secret"}
	h := newCommandHandler(cfg, execStub{resp: api.CommandResponse{Ok: true}})

	req := httptest.NewRequest(http.MethodPost, "/command", bytes.NewBufferString("{}"))
	w := httptest.NewRecorder()
	h(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", w.Code)
	}
}

func TestCommandHandlerReturnsExecutorResponse(t *testing.T) {
	cfg := &AgentConfig{}
	resp := api.CommandResponse{Ok: true, ExitCode: 0, Stdout: "ok"}
	h := newCommandHandler(cfg, execStub{resp: resp})

	body, _ := json.Marshal(api.CommandRequest{Command: "status"})
	req := httptest.NewRequest(http.MethodPost, "/command", bytes.NewReader(body))
	w := httptest.NewRecorder()
	h(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
}
