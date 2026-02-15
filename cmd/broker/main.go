package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

type BrokerConfig struct {
	ListenAddr            string   `json:"listen_addr"`
	TelegramBotToken      string   `json:"telegram_bot_token"`
	TelegramMode          string   `json:"telegram_mode"`
	TelegramWebhookPath   string   `json:"telegram_webhook_path"`
	TelegramAllowedUserIDs []int64 `json:"telegram_allowed_user_ids"`
	ForwardURL            string   `json:"forward_url"`
	ForwardAuthToken      string   `json:"forward_auth_token"`
	RateLimitPerMinute    int      `json:"rate_limit_per_minute"`
	CommandAllowlist      []string `json:"command_allowlist"`
	CommandBlocklist      []string `json:"command_blocklist"`
	PollIntervalSec       int      `json:"poll_interval_sec"`
}

type TelegramUpdate struct {
	UpdateID int64            `json:"update_id"`
	Message  *TelegramMessage `json:"message"`
}

type TelegramUpdatesResponse struct {
	Ok     bool             `json:"ok"`
	Result []TelegramUpdate `json:"result"`
}

type TelegramMessage struct {
	MessageID int64         `json:"message_id"`
	From      TelegramUser  `json:"from"`
	Chat      TelegramChat  `json:"chat"`
	Date      int64         `json:"date"`
	Text      string        `json:"text"`
}

type TelegramUser struct {
	ID        int64  `json:"id"`
	UserName  string `json:"username"`
	FirstName string `json:"first_name"`
}

type TelegramChat struct {
	ID   int64  `json:"id"`
	Type string `json:"type"`
}

type CommandRequest struct {
	Command string `json:"command"`
	UserID  int64  `json:"user_id"`
	ChatID  int64  `json:"chat_id"`
	Text    string `json:"text"`
	Args    []string `json:"args"`
}

type CommandResponse struct {
	Ok       bool   `json:"ok"`
	ExitCode int    `json:"exit_code"`
	Stdout   string `json:"stdout"`
	Stderr   string `json:"stderr"`
	Error    string `json:"error"`
}

type rateLimiter struct {
	mu     sync.Mutex
	window time.Duration
	max    int
	stamp  map[int64][]time.Time
}

func newRateLimiter(window time.Duration, max int) *rateLimiter {
	return &rateLimiter{window: window, max: max, stamp: make(map[int64][]time.Time)}
}

func (r *rateLimiter) allow(userID int64) bool {
	if r.max <= 0 {
		return true
	}
	now := time.Now()
	cut := now.Add(-r.window)

	r.mu.Lock()
	defer r.mu.Unlock()
	list := r.stamp[userID]
	out := list[:0]
	for _, t := range list {
		if t.After(cut) {
			out = append(out, t)
		}
	}
	if len(out) >= r.max {
		r.stamp[userID] = out
		return false
	}
	out = append(out, now)
	r.stamp[userID] = out
	return true
}

func loadConfig(path string) (*BrokerConfig, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var cfg BrokerConfig
	if err := json.Unmarshal(b, &cfg); err != nil {
		return nil, err
	}
	if cfg.ListenAddr == "" {
		cfg.ListenAddr = "127.0.0.1:8081"
	}
	if cfg.TelegramMode == "" {
		cfg.TelegramMode = "webhook"
	}
	if cfg.TelegramWebhookPath == "" {
		cfg.TelegramWebhookPath = "/telegram/webhook"
	}
	if cfg.RateLimitPerMinute <= 0 {
		cfg.RateLimitPerMinute = 20
	}
	if cfg.PollIntervalSec <= 0 {
		cfg.PollIntervalSec = 3
	}
	return &cfg, nil
}

func isAllowed(userID int64, allowed []int64) bool {
	for _, id := range allowed {
		if id == userID {
			return true
		}
	}
	return false
}

func isCommandAllowed(cmd string, allow []string) bool {
	for _, c := range allow {
		if strings.EqualFold(cmd, c) {
			return true
		}
	}
	return false
}

func isCommandBlocked(cmd string, block []string) bool {
	for _, c := range block {
		if strings.EqualFold(cmd, c) {
			return true
		}
	}
	return false
}

func main() {
	configPath := flag.String("config", "configs/broker.json", "path to broker config json")
	flag.Parse()

	cfg, err := loadConfig(*configPath)
	if err != nil {
		log.Fatalf("load config: %v", err)
	}

	rl := newRateLimiter(time.Minute, cfg.RateLimitPerMinute)

	mode := strings.ToLower(strings.TrimSpace(cfg.TelegramMode))
	if mode == "polling" {
		log.Printf("broker starting in polling mode")
		pollLoop(cfg, rl)
		return
	}

	mux := http.NewServeMux()
	mux.HandleFunc(cfg.TelegramWebhookPath, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		var update TelegramUpdate
		if err := json.Unmarshal(body, &update); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}

		processUpdate(cfg, rl, update)
		w.WriteHeader(http.StatusOK)
	})

	srv := &http.Server{
		Addr:              cfg.ListenAddr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}

	log.Printf("broker listening on %s (webhook mode)", cfg.ListenAddr)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("server: %v", err)
	}
}

func processUpdate(cfg *BrokerConfig, rl *rateLimiter, update TelegramUpdate) {
	if update.Message == nil {
		return
	}

	msg := update.Message
	userID := msg.From.ID
	chatID := msg.Chat.ID

	if !isAllowed(userID, cfg.TelegramAllowedUserIDs) {
		_ = sendTelegramMessage(cfg.TelegramBotToken, chatID, "Unauthorized user.")
		return
	}

	if !rl.allow(userID) {
		_ = sendTelegramMessage(cfg.TelegramBotToken, chatID, "Rate limit exceeded. Try again soon.")
		return
	}

		cmd, args := normalizeCommand(msg.Text)
		if cmd == "" {
			_ = sendTelegramMessage(cfg.TelegramBotToken, chatID, "Empty command.")
			return
		}
	if cmd == "help" {
		_ = sendTelegramMessage(cfg.TelegramBotToken, chatID, "Allowed commands: "+strings.Join(cfg.CommandAllowlist, ", "))
		return
	}
	if isCommandBlocked(cmd, cfg.CommandBlocklist) {
		_ = sendTelegramMessage(cfg.TelegramBotToken, chatID, "Command blocked.")
		return
	}
	if !isCommandAllowed(cmd, cfg.CommandAllowlist) {
		_ = sendTelegramMessage(cfg.TelegramBotToken, chatID, "Command not allowed.")
		return
	}

		resp, err := forwardCommand(cfg, CommandRequest{Command: cmd, UserID: userID, ChatID: chatID, Text: msg.Text, Args: args})
	if err != nil {
		_ = sendTelegramMessage(cfg.TelegramBotToken, chatID, "Agent error: "+err.Error())
		return
	}

	reply := renderResponse(cmd, resp)
	if err := sendTelegramMessage(cfg.TelegramBotToken, chatID, reply); err != nil {
		log.Printf("send telegram: %v", err)
	}
}

func pollLoop(cfg *BrokerConfig, rl *rateLimiter) {
	client := &http.Client{Timeout: 35 * time.Second}
	var offset int64
	for {
		updates, err := getUpdates(client, cfg.TelegramBotToken, offset)
		if err != nil {
			log.Printf("getUpdates error: %v", err)
			time.Sleep(time.Duration(cfg.PollIntervalSec) * time.Second)
			continue
		}
		for _, upd := range updates {
			processUpdate(cfg, rl, upd)
			if upd.UpdateID >= offset {
				offset = upd.UpdateID + 1
			}
		}
		if len(updates) == 0 {
			time.Sleep(time.Duration(cfg.PollIntervalSec) * time.Second)
		}
	}
}

func getUpdates(client *http.Client, token string, offset int64) ([]TelegramUpdate, error) {
	url := fmt.Sprintf("https://api.telegram.org/bot%s/getUpdates", token)
	payload := map[string]any{
		"offset":         offset,
		"timeout":        30,
		"allowed_updates": []string{"message"},
	}
	body, _ := json.Marshal(payload)
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<10))
		return nil, fmt.Errorf("telegram status %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	var tr TelegramUpdatesResponse
	if err := json.Unmarshal(respBody, &tr); err != nil {
		return nil, err
	}
	if !tr.Ok {
		return nil, fmt.Errorf("telegram returned ok=false")
	}
	return tr.Result, nil
}

func normalizeCommand(text string) (string, []string) {
	parts := strings.Fields(strings.TrimSpace(text))
	if len(parts) == 0 {
		return "", nil
	}
	cmd := parts[0]
	cmd = strings.TrimPrefix(cmd, "/")
	cmd = strings.ToLower(cmd)
	if len(parts) == 1 {
		return cmd, nil
	}
	return cmd, parts[1:]
}

func forwardCommand(cfg *BrokerConfig, req CommandRequest) (*CommandResponse, error) {
	body, _ := json.Marshal(req)
	httpReq, err := http.NewRequest(http.MethodPost, cfg.ForwardURL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if cfg.ForwardAuthToken != "" {
		httpReq.Header.Set("X-Auth-Token", cfg.ForwardAuthToken)
	}

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("agent status %d", resp.StatusCode)
	}
	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	var cr CommandResponse
	if err := json.Unmarshal(respBody, &cr); err != nil {
		return nil, err
	}
	return &cr, nil
}

func renderResponse(cmd string, resp *CommandResponse) string {
	if resp.Ok {
		out := strings.TrimSpace(resp.Stdout)
		if out == "" {
			out = "(no output)"
		}
		return fmt.Sprintf("%s:\n%s", cmd, out)
	}

	errMsg := resp.Error
	if errMsg == "" {
		errMsg = "command failed"
	}
	out := strings.TrimSpace(resp.Stderr)
	if out == "" {
		out = strings.TrimSpace(resp.Stdout)
	}
	if out != "" {
		return fmt.Sprintf("%s failed (exit %d): %s\n%s", cmd, resp.ExitCode, errMsg, out)
	}
	return fmt.Sprintf("%s failed (exit %d): %s", cmd, resp.ExitCode, errMsg)
}

func sendTelegramMessage(token string, chatID int64, text string) error {
	if token == "" {
		return fmt.Errorf("telegram bot token missing")
	}
	url := fmt.Sprintf("https://api.telegram.org/bot%s/sendMessage", token)
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

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
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
