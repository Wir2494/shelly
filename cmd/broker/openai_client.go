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

type openAIClient struct {
	apiKey    string
	model     string
	timeout   time.Duration
	baseURL   string
	client    *http.Client
	maxBodyKB int64
}

func newOpenAIClient(cfg LLMConfig) *openAIClient {
	model := strings.TrimSpace(cfg.Model)
	if model == "" {
		model = "gpt-5.2"
	}
	return &openAIClient{
		apiKey:    cfg.APIKey,
		model:     model,
		timeout:   time.Duration(cfg.TimeoutSec) * time.Second,
		baseURL:   "https://api.openai.com/v1/responses",
		client:    &http.Client{Timeout: time.Duration(cfg.TimeoutSec) * time.Second},
		maxBodyKB: 1024,
	}
}

func (c *openAIClient) Map(ctx context.Context, userText string, allowlist []string) (*api.LLMDecision, error) {
	if strings.TrimSpace(c.apiKey) == "" {
		return nil, fmt.Errorf("llm.api_key is not set")
	}
	if c.timeout == 0 {
		c.timeout = 15 * time.Second
	}
	if c.client == nil {
		c.client = &http.Client{Timeout: c.timeout}
	}
	if c.baseURL == "" {
		c.baseURL = "https://api.openai.com/v1/responses"
	}
	if c.maxBodyKB == 0 {
		c.maxBodyKB = 1024
	}

	systemPrompt := "You are a command router. Decide whether the user wants to run an allowed command or just chat. " +
		"If it is a command, map it to one of these intents: " + strings.Join(allowlist, ", ") + ". " +
		"Commands may include dynamic filesystem actions (pwd, ls/ll, cd, cat, touch, mkdir, count, find) and ping, " +
		"but always stay within the configured base directory when using paths. " +
		"Return JSON only that matches the provided schema. If it is chat, respond in the 'response' field."

	reqBody := map[string]any{
		"model": c.model,
		"input": []any{
			map[string]any{
				"role": "system",
				"content": []any{
					map[string]any{"type": "input_text", "text": systemPrompt},
				},
			},
			map[string]any{
				"role": "user",
				"content": []any{
					map[string]any{"type": "input_text", "text": userText},
				},
			},
		},
		"text": map[string]any{
			"format": map[string]any{
				"type": "json_schema",
				"name": "telegram_intent",
				"schema": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"type": map[string]any{
							"type": "string",
							"enum": []string{"command", "chat"},
						},
						"intent": map[string]any{"type": "string"},
						"args": map[string]any{
							"type":  "array",
							"items": map[string]any{"type": "string"},
						},
						"response": map[string]any{"type": "string"},
						"confidence": map[string]any{
							"type":    "number",
							"minimum": 0,
							"maximum": 1,
						},
					},
					"required":             []string{"type", "intent", "args", "response", "confidence"},
					"additionalProperties": false,
				},
			},
		},
	}

	payload, _ := json.Marshal(reqBody)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<12))
		return nil, fmt.Errorf("llm status %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}

	var parsed struct {
		Output []struct {
			Type    string `json:"type"`
			Role    string `json:"role"`
			Status  string `json:"status"`
			Content []struct {
				Type    string `json:"type"`
				Text    string `json:"text"`
				Refusal string `json:"refusal"`
			} `json:"content"`
		} `json:"output"`
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, c.maxBodyKB*1024))
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, err
	}

	for _, out := range parsed.Output {
		if out.Type != "message" {
			continue
		}
		for _, c := range out.Content {
			if c.Type == "output_text" && strings.TrimSpace(c.Text) != "" {
				var decision api.LLMDecision
				if err := json.Unmarshal([]byte(c.Text), &decision); err != nil {
					return nil, fmt.Errorf("llm json parse error: %v", err)
				}
				return &decision, nil
			}
			if c.Type == "refusal" && strings.TrimSpace(c.Refusal) != "" {
				return nil, fmt.Errorf("llm refused: %s", c.Refusal)
			}
		}
	}

	return nil, fmt.Errorf("llm returned no usable output")
}
