package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"personal_ai/internal/api"
)

func TestRemoteExecutorSendsAuthAndParsesResponse(t *testing.T) {
	var gotAuth string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("X-Auth-Token")
		var req api.CommandRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if req.Command != "status" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		_ = json.NewEncoder(w).Encode(api.CommandResponse{Ok: true, ExitCode: 0, Stdout: "ok"})
	}))
	defer server.Close()

	cfg := &BrokerConfig{Execution: ExecutionConfig{ForwardURL: server.URL, ForwardAuthToken: "secret"}}
	exec := newRemoteExecutor(cfg)

	resp, err := exec.Execute(context.Background(), api.CommandRequest{Command: "status"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotAuth != "secret" {
		t.Fatalf("expected auth header to be set")
	}
	if resp.Stdout != "ok" || !resp.Ok {
		t.Fatalf("unexpected response: %+v", resp)
	}
}

func TestRemoteExecutorNonOKStatusReturnsError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	cfg := &BrokerConfig{Execution: ExecutionConfig{ForwardURL: server.URL}}
	exec := newRemoteExecutor(cfg)

	_, err := exec.Execute(context.Background(), api.CommandRequest{Command: "status"})
	if err == nil {
		t.Fatalf("expected error")
	}
}
