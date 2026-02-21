package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type telegramSender struct {
	token  string
	client *http.Client
}

func newTelegramSender(token string) *telegramSender {
	return &telegramSender{
		token:  token,
		client: &http.Client{Timeout: 10 * time.Second},
	}
}

func (s *telegramSender) Send(chatID int64, text string) error {
	if s.token == "" {
		return fmt.Errorf("telegram bot token missing")
	}
	url := fmt.Sprintf("https://api.telegram.org/bot%s/sendMessage", s.token)
	payload := map[string]any{
		"chat_id": chatID,
		"text":    text,
	}
	body, _ := json.Marshal(payload)

	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := s.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<10))
		return fmt.Errorf("telegram status %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	return nil
}
