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
	"sort"
	"strings"
	"sync"
	"time"
)

type BrokerConfig struct {
	ListenAddr             string                    `json:"listen_addr"`
	TelegramBotToken       string                    `json:"telegram_bot_token"`
	TelegramMode           string                    `json:"telegram_mode"`
	TelegramWebhookPath    string                    `json:"telegram_webhook_path"`
	TelegramAllowedUserIDs []int64                   `json:"telegram_allowed_user_ids"`
	ExecutionMode          string                    `json:"execution_mode"`
	ForwardURL             string                    `json:"forward_url"`
	ForwardAuthToken       string                    `json:"forward_auth_token"`
	RateLimitPerMinute     int                       `json:"rate_limit_per_minute"`
	CommandAllowlist       []string                  `json:"command_allowlist"`
	CommandBlocklist       []string                  `json:"command_blocklist"`
	PollIntervalSec        int                       `json:"poll_interval_sec"`
	LLMEnabled             bool                      `json:"llm_enabled"`
	LLMAPIKey              string                    `json:"llm_api_key"`
	LLMModel               string                    `json:"llm_model"`
	LLMTimeoutSec          int                       `json:"llm_timeout_sec"`
	LLMConfidenceThreshold float64                   `json:"llm_confidence_threshold"`
	LocalDefaultTimeoutSec int                       `json:"local_default_timeout_sec"`
	LocalMaxOutputKB       int                       `json:"local_max_output_kb"`
	LocalCommandAllowlist  map[string]AllowedCommand `json:"local_command_allowlist"`
	LocalDynamicAllowlist  []string                  `json:"local_dynamic_allowlist"`
	LocalBaseDir           string                    `json:"local_base_dir"`
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
	MessageID int64        `json:"message_id"`
	From      TelegramUser `json:"from"`
	Chat      TelegramChat `json:"chat"`
	Date      int64        `json:"date"`
	Text      string       `json:"text"`
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
	Command string   `json:"command"`
	UserID  int64    `json:"user_id"`
	ChatID  int64    `json:"chat_id"`
	Text    string   `json:"text"`
	Args    []string `json:"args"`
}

type CommandResponse struct {
	Ok       bool   `json:"ok"`
	ExitCode int    `json:"exit_code"`
	Stdout   string `json:"stdout"`
	Stderr   string `json:"stderr"`
	Error    string `json:"error"`
}

type LLMDecision struct {
	Type       string   `json:"type"`
	Intent     string   `json:"intent"`
	Args       []string `json:"args"`
	Response   string   `json:"response"`
	Confidence float64  `json:"confidence"`
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
	if cfg.ExecutionMode == "" {
		if strings.TrimSpace(cfg.ForwardURL) == "" {
			cfg.ExecutionMode = "local"
		} else {
			cfg.ExecutionMode = "forward"
		}
	}
	if cfg.RateLimitPerMinute <= 0 {
		cfg.RateLimitPerMinute = 20
	}
	if cfg.PollIntervalSec <= 0 {
		cfg.PollIntervalSec = 3
	}
	if cfg.LLMTimeoutSec <= 0 {
		cfg.LLMTimeoutSec = 15
	}
	if cfg.LLMConfidenceThreshold <= 0 {
		cfg.LLMConfidenceThreshold = 0.7
	}
	if cfg.LocalDefaultTimeoutSec <= 0 {
		cfg.LocalDefaultTimeoutSec = 10
	}
	if cfg.LocalMaxOutputKB <= 0 {
		cfg.LocalMaxOutputKB = 8
	}
	if len(cfg.CommandAllowlist) == 0 && (len(cfg.LocalCommandAllowlist) > 0 || len(cfg.LocalDynamicAllowlist) > 0) {
		cfg.CommandAllowlist = buildAllowlistFromLocal(cfg.LocalCommandAllowlist, cfg.LocalDynamicAllowlist)
	}
	return &cfg, nil
}

func buildAllowlistFromLocal(static map[string]AllowedCommand, dynamic []string) []string {
	seen := make(map[string]struct{})
	for name := range static {
		seen[strings.ToLower(name)] = struct{}{}
	}
	for _, name := range dynamic {
		seen[strings.ToLower(name)] = struct{}{}
	}
	out := make([]string, 0, len(seen))
	for name := range seen {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
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

type commandExecutor func(req CommandRequest) (*CommandResponse, error)

func validateExecutionConfig(cfg *BrokerConfig) error {
	mode := strings.ToLower(strings.TrimSpace(cfg.ExecutionMode))
	switch mode {
	case "local":
		if len(cfg.LocalCommandAllowlist) == 0 && len(cfg.LocalDynamicAllowlist) == 0 {
			return fmt.Errorf("local mode requires local_command_allowlist or local_dynamic_allowlist")
		}
	case "forward":
		if strings.TrimSpace(cfg.ForwardURL) == "" {
			return fmt.Errorf("forward_url required when execution_mode is forward")
		}
	default:
		return fmt.Errorf("unsupported execution_mode: %s", cfg.ExecutionMode)
	}
	return nil
}

func buildExecutor(cfg *BrokerConfig) commandExecutor {
	mode := strings.ToLower(strings.TrimSpace(cfg.ExecutionMode))
	if mode == "local" {
		local := newLocalExecutor(cfg)
		return local.Execute
	}
	return func(req CommandRequest) (*CommandResponse, error) {
		return forwardCommand(cfg, req)
	}
}

func main() {
	configPath := flag.String("config", "configs/broker.json", "path to broker config json")
	flag.Parse()

	cfg, err := loadConfig(*configPath)
	if err != nil {
		log.Fatalf("load config: %v", err)
	}
	if err := validateExecutionConfig(cfg); err != nil {
		log.Fatalf("config validation: %v", err)
	}

	rl := newRateLimiter(time.Minute, cfg.RateLimitPerMinute)
	exec := buildExecutor(cfg)

	mode := strings.ToLower(strings.TrimSpace(cfg.TelegramMode))
	if mode == "polling" {
		log.Printf("broker starting in polling mode")
		pollLoop(cfg, rl, exec)
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

		processUpdate(cfg, rl, exec, update)
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

func processUpdate(cfg *BrokerConfig, rl *rateLimiter, exec commandExecutor, update TelegramUpdate) {
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

	if cfg.LLMEnabled {
		decision, err := mapWithLLM(cfg, msg.Text)
		if err != nil {
			_ = sendTelegramMessage(cfg.TelegramBotToken, chatID, "LLM error: "+err.Error())
			return
		}

		if strings.EqualFold(decision.Type, "chat") {
			if strings.TrimSpace(decision.Response) == "" {
				_ = sendTelegramMessage(cfg.TelegramBotToken, chatID, "I didn't understand that. Try a command or ask again.")
			} else {
				_ = sendTelegramMessage(cfg.TelegramBotToken, chatID, decision.Response)
			}
			return
		}

		cmd := strings.ToLower(strings.TrimSpace(decision.Intent))
		if cmd == "" {
			_ = sendTelegramMessage(cfg.TelegramBotToken, chatID, "I couldn't determine a command. Try again.")
			return
		}
		if decision.Confidence < cfg.LLMConfidenceThreshold {
			_ = sendTelegramMessage(cfg.TelegramBotToken, chatID, "I am not confident this is a command. Please rephrase or use a direct command.")
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

		resp, err := exec(CommandRequest{Command: cmd, UserID: userID, ChatID: chatID, Text: msg.Text, Args: decision.Args})
		if err != nil {
			_ = sendTelegramMessage(cfg.TelegramBotToken, chatID, "Agent error: "+err.Error())
			return
		}

		reply := renderResponse(cmd, resp)
		if err := sendTelegramMessage(cfg.TelegramBotToken, chatID, reply); err != nil {
			log.Printf("send telegram: %v", err)
		}
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

	resp, err := exec(CommandRequest{Command: cmd, UserID: userID, ChatID: chatID, Text: msg.Text, Args: args})
	if err != nil {
		_ = sendTelegramMessage(cfg.TelegramBotToken, chatID, "Agent error: "+err.Error())
		return
	}

	reply := renderResponse(cmd, resp)
	if err := sendTelegramMessage(cfg.TelegramBotToken, chatID, reply); err != nil {
		log.Printf("send telegram: %v", err)
	}
}

func pollLoop(cfg *BrokerConfig, rl *rateLimiter, exec commandExecutor) {
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
			processUpdate(cfg, rl, exec, upd)
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
		"offset":          offset,
		"timeout":         30,
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

func mapWithLLM(cfg *BrokerConfig, userText string) (*LLMDecision, error) {
	if strings.TrimSpace(cfg.LLMAPIKey) == "" {
		return nil, fmt.Errorf("llm_api_key is not set")
	}
	model := cfg.LLMModel
	if strings.TrimSpace(model) == "" {
		model = "gpt-5.2"
	}

	systemPrompt := "You are a command router. Decide whether the user wants to run an allowed command or just chat. " +
		"If it is a command, map it to one of these intents: " + strings.Join(cfg.CommandAllowlist, ", ") + ". " +
		"Return JSON only that matches the provided schema. If it is chat, respond in the 'response' field."

	reqBody := map[string]any{
		"model": model,
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
	req, err := http.NewRequest(http.MethodPost, "https://api.openai.com/v1/responses", bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+cfg.LLMAPIKey)
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: time.Duration(cfg.LLMTimeoutSec) * time.Second}
	resp, err := client.Do(req)
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
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
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
				var decision LLMDecision
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
