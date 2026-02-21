package main

import (
	"bytes"
	"context"
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

	"personal_ai/internal/api"
)

type BrokerConfig struct {
	ListenAddr string          `json:"listen_addr"`
	Telegram   TelegramConfig  `json:"telegram"`
	Execution  ExecutionConfig `json:"execution"`
	LLM        LLMConfig       `json:"llm"`
	Policy     PolicyConfig    `json:"policy"`
}

type TelegramConfig struct {
	BotToken        string  `json:"bot_token"`
	Mode            string  `json:"mode"`
	WebhookPath     string  `json:"webhook_path"`
	AllowedUserIDs  []int64 `json:"allowed_user_ids"`
	PollIntervalSec int     `json:"poll_interval_sec"`
}

type ExecutionConfig struct {
	Mode             string               `json:"mode"`
	ForwardURL       string               `json:"forward_url"`
	ForwardAuthToken string               `json:"forward_auth_token"`
	Local            LocalExecutionConfig `json:"local"`
}

type LocalExecutionConfig struct {
	DefaultTimeoutSec int                           `json:"default_timeout_sec"`
	MaxOutputKB       int                           `json:"max_output_kb"`
	BaseDir           string                        `json:"base_dir"`
	DynamicAllowlist  []string                      `json:"dynamic_allowlist"`
	CommandAllowlist  map[string]api.AllowedCommand `json:"command_allowlist"`
}

type LLMConfig struct {
	Enabled             bool    `json:"enabled"`
	APIKey              string  `json:"api_key"`
	Model               string  `json:"model"`
	TimeoutSec          int     `json:"timeout_sec"`
	ConfidenceThreshold float64 `json:"confidence_threshold"`
}

type PolicyConfig struct {
	RateLimitPerMinute int      `json:"rate_limit_per_minute"`
	CommandAllowlist   []string `json:"command_allowlist"`
	CommandBlocklist   []string `json:"command_blocklist"`
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
	if cfg.Telegram.Mode == "" {
		cfg.Telegram.Mode = "polling"
	}
	if cfg.Telegram.WebhookPath == "" {
		cfg.Telegram.WebhookPath = "/telegram/webhook"
	}
	if cfg.Execution.Mode == "" {
		if strings.TrimSpace(cfg.Execution.ForwardURL) == "" {
			cfg.Execution.Mode = "local"
		} else {
			cfg.Execution.Mode = "forward"
		}
	}
	if cfg.Policy.RateLimitPerMinute <= 0 {
		cfg.Policy.RateLimitPerMinute = 20
	}
	if cfg.Telegram.PollIntervalSec <= 0 {
		cfg.Telegram.PollIntervalSec = 3
	}
	if cfg.LLM.TimeoutSec <= 0 {
		cfg.LLM.TimeoutSec = 15
	}
	if cfg.LLM.ConfidenceThreshold <= 0 {
		cfg.LLM.ConfidenceThreshold = 0.7
	}
	if cfg.Execution.Local.DefaultTimeoutSec <= 0 {
		cfg.Execution.Local.DefaultTimeoutSec = 10
	}
	if cfg.Execution.Local.MaxOutputKB <= 0 {
		cfg.Execution.Local.MaxOutputKB = 8
	}
	if len(cfg.Policy.CommandAllowlist) == 0 && (len(cfg.Execution.Local.CommandAllowlist) > 0 || len(cfg.Execution.Local.DynamicAllowlist) > 0) {
		cfg.Policy.CommandAllowlist = buildAllowlistFromLocal(cfg.Execution.Local.CommandAllowlist, cfg.Execution.Local.DynamicAllowlist)
	}
	return &cfg, nil
}

func buildAllowlistFromLocal(static map[string]api.AllowedCommand, dynamic []string) []string {
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

type Executor interface {
	Execute(ctx context.Context, req api.CommandRequest) (*api.CommandResponse, error)
}

type TelegramSender interface {
	Send(chatID int64, text string) error
}

type LLMClient interface {
	Map(ctx context.Context, userText string, allowlist []string) (*api.LLMDecision, error)
}

type pipelineContext struct {
	cfg    *BrokerConfig
	rl     *rateLimiter
	exec   Executor
	update TelegramUpdate
	msg    *TelegramMessage
	userID int64
	chatID int64
	cmd    string
	args   []string
	sender TelegramSender
	llm    LLMClient
}

type pipelineStage func(*pipelineContext) bool

type Broker struct {
	cfg    *BrokerConfig
	rl     *rateLimiter
	exec   Executor
	sender TelegramSender
	llm    LLMClient
}

func newBroker(cfg *BrokerConfig, rl *rateLimiter, exec Executor, sender TelegramSender, llm LLMClient) *Broker {
	return &Broker{cfg: cfg, rl: rl, exec: exec, sender: sender, llm: llm}
}

func validateExecutionConfig(cfg *BrokerConfig) error {
	mode := strings.ToLower(strings.TrimSpace(cfg.Execution.Mode))
	switch mode {
	case "local":
		if len(cfg.Execution.Local.CommandAllowlist) == 0 && len(cfg.Execution.Local.DynamicAllowlist) == 0 {
			return fmt.Errorf("local mode requires execution.local.command_allowlist or execution.local.dynamic_allowlist")
		}
	case "forward":
		if strings.TrimSpace(cfg.Execution.ForwardURL) == "" {
			return fmt.Errorf("execution.forward_url required when execution.mode is forward")
		}
	default:
		return fmt.Errorf("unsupported execution.mode: %s", cfg.Execution.Mode)
	}
	return nil
}

func buildExecutor(cfg *BrokerConfig) Executor {
	mode := strings.ToLower(strings.TrimSpace(cfg.Execution.Mode))
	if mode == "local" {
		return newLocalExecutor(cfg)
	}
	return newRemoteExecutor(cfg)
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

	rl := newRateLimiter(time.Minute, cfg.Policy.RateLimitPerMinute)
	exec := buildExecutor(cfg)
	sender := newTelegramSender(cfg.Telegram.BotToken)
	llm := newOpenAIClient(cfg.LLM)
	broker := newBroker(cfg, rl, exec, sender, llm)

	mode := strings.ToLower(strings.TrimSpace(cfg.Telegram.Mode))
	if mode == "polling" {
		log.Printf("broker starting in polling mode")
		broker.pollLoop()
		return
	}

	mux := http.NewServeMux()
	mux.HandleFunc(cfg.Telegram.WebhookPath, func(w http.ResponseWriter, r *http.Request) {
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

		broker.processUpdate(update)
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

func (b *Broker) processUpdate(update TelegramUpdate) {
	ctx := &pipelineContext{
		cfg:    b.cfg,
		rl:     b.rl,
		exec:   b.exec,
		update: update,
		sender: b.sender,
		llm:    b.llm,
	}

	stages := []pipelineStage{
		stageExtractMessage,
		stageAuth,
		stageRateLimit,
		stageRoute,
		stagePolicy,
		stageExecute,
	}

	for _, stage := range stages {
		if stop := stage(ctx); stop {
			return
		}
	}
}

func stageExtractMessage(ctx *pipelineContext) bool {
	if ctx.update.Message == nil {
		return true
	}
	ctx.msg = ctx.update.Message
	ctx.userID = ctx.msg.From.ID
	ctx.chatID = ctx.msg.Chat.ID
	return false
}

func stageAuth(ctx *pipelineContext) bool {
	if !isAllowed(ctx.userID, ctx.cfg.Telegram.AllowedUserIDs) {
		return sendReply(ctx, "Unauthorized user.")
	}
	return false
}

func stageRateLimit(ctx *pipelineContext) bool {
	if !ctx.rl.allow(ctx.userID) {
		return sendReply(ctx, "Rate limit exceeded. Try again soon.")
	}
	return false
}

func stageRoute(ctx *pipelineContext) bool {
	if ctx.cfg.LLM.Enabled {
		if ctx.llm == nil {
			return sendReply(ctx, "LLM error: client not configured")
		}
		decision, err := ctx.llm.Map(context.Background(), ctx.msg.Text, ctx.cfg.Policy.CommandAllowlist)
		if err != nil {
			return sendReply(ctx, "LLM error: "+err.Error())
		}

		if strings.EqualFold(decision.Type, "chat") {
			resp := strings.TrimSpace(decision.Response)
			if resp == "" {
				return sendReply(ctx, "I didn't understand that. Try a command or ask again.")
			}
			return sendReply(ctx, resp)
		}

		cmd := strings.ToLower(strings.TrimSpace(decision.Intent))
		if cmd == "" {
			return sendReply(ctx, "I couldn't determine a command. Try again.")
		}
		if decision.Confidence < ctx.cfg.LLM.ConfidenceThreshold {
			return sendReply(ctx, "I am not confident this is a command. Please rephrase or use a direct command.")
		}
		if cmd == "help" {
			return sendReply(ctx, "Allowed commands: "+strings.Join(ctx.cfg.Policy.CommandAllowlist, ", "))
		}
		ctx.cmd = cmd
		ctx.args = decision.Args
		return false
	}

	cmd, args := normalizeCommand(ctx.msg.Text)
	if cmd == "" {
		return sendReply(ctx, "Empty command.")
	}
	if cmd == "help" {
		return sendReply(ctx, "Allowed commands: "+strings.Join(ctx.cfg.Policy.CommandAllowlist, ", "))
	}
	ctx.cmd = cmd
	ctx.args = args
	return false
}

func stagePolicy(ctx *pipelineContext) bool {
	if isCommandBlocked(ctx.cmd, ctx.cfg.Policy.CommandBlocklist) {
		return sendReply(ctx, "Command blocked.")
	}
	if !isCommandAllowed(ctx.cmd, ctx.cfg.Policy.CommandAllowlist) {
		return sendReply(ctx, "Command not allowed.")
	}
	return false
}

func stageExecute(ctx *pipelineContext) bool {
	resp, err := ctx.exec.Execute(context.Background(), api.CommandRequest{
		Command: ctx.cmd,
		UserID:  ctx.userID,
		ChatID:  ctx.chatID,
		Text:    ctx.msg.Text,
		Args:    ctx.args,
	})
	if err != nil {
		return sendReply(ctx, "Agent error: "+err.Error())
	}

	reply := renderResponse(ctx.cmd, resp)
	return sendReply(ctx, reply)
}

func sendReply(ctx *pipelineContext, text string) bool {
	if err := ctx.sender.Send(ctx.chatID, text); err != nil {
		log.Printf("send telegram: %v", err)
	}
	return true
}

func (b *Broker) pollLoop() {
	client := &http.Client{Timeout: 35 * time.Second}
	var offset int64
	for {
		updates, err := getUpdates(client, b.cfg.Telegram.BotToken, offset)
		if err != nil {
			log.Printf("getUpdates error: %v", err)
			time.Sleep(time.Duration(b.cfg.Telegram.PollIntervalSec) * time.Second)
			continue
		}
		for _, upd := range updates {
			b.processUpdate(upd)
			if upd.UpdateID >= offset {
				offset = upd.UpdateID + 1
			}
		}
		if len(updates) == 0 {
			time.Sleep(time.Duration(b.cfg.Telegram.PollIntervalSec) * time.Second)
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

func renderResponse(cmd string, resp *api.CommandResponse) string {
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
