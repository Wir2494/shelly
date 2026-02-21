package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"personal_ai/internal/api"
)

type remoteExecutor struct {
	forwardURL   string
	authToken    string
	client       *http.Client
	maxBodyBytes int64
}

func newRemoteExecutor(cfg *BrokerConfig) *remoteExecutor {
	return &remoteExecutor{
		forwardURL:   cfg.ForwardURL,
		authToken:    cfg.ForwardAuthToken,
		client:       &http.Client{Timeout: 15 * time.Second},
		maxBodyBytes: 1 << 20,
	}
}

func (e *remoteExecutor) Execute(ctx context.Context, req api.CommandRequest) (*api.CommandResponse, error) {
	body, _ := json.Marshal(req)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, e.forwardURL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if e.authToken != "" {
		httpReq.Header.Set("X-Auth-Token", e.authToken)
	}

	resp, err := e.client.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("agent status %d", resp.StatusCode)
	}
	respBody, err := io.ReadAll(io.LimitReader(resp.Body, e.maxBodyBytes))
	if err != nil {
		return nil, err
	}
	var cr api.CommandResponse
	if err := json.Unmarshal(respBody, &cr); err != nil {
		return nil, err
	}
	if strings.TrimSpace(cr.Error) != "" {
		return &cr, nil
	}
	return &cr, nil
}
