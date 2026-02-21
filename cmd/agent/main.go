package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"personal_ai/internal/api"
)

type AgentConfig struct {
	ListenAddr string          `json:"listen_addr"`
	AuthToken  string          `json:"auth_token"`
	Execution  AgentExecConfig `json:"execution"`
}

type AgentExecConfig struct {
	DefaultTimeoutSec int                           `json:"default_timeout_sec"`
	MaxOutputKB       int                           `json:"max_output_kb"`
	CommandAllowlist  map[string]api.AllowedCommand `json:"command_allowlist"`
	CommandBlocklist  []string                      `json:"command_blocklist"`
	DynamicAllowlist  []string                      `json:"dynamic_allowlist"`
	BaseDir           string                        `json:"base_dir"`
}

func loadConfig(path string) (*AgentConfig, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var cfg AgentConfig
	if err := json.Unmarshal(b, &cfg); err != nil {
		return nil, err
	}
	if cfg.ListenAddr == "" {
		cfg.ListenAddr = "127.0.0.1:8080"
	}
	if cfg.Execution.DefaultTimeoutSec <= 0 {
		cfg.Execution.DefaultTimeoutSec = 10
	}
	if cfg.Execution.MaxOutputKB <= 0 {
		cfg.Execution.MaxOutputKB = 8
	}
	return &cfg, nil
}

func isBlocked(cmd string, blocklist []string) bool {
	for _, b := range blocklist {
		if strings.EqualFold(cmd, b) {
			return true
		}
	}
	return false
}

func main() {
	configPath := flag.String("config", "configs/agent.json", "path to agent config json")
	flag.Parse()

	cfg, err := loadConfig(*configPath)
	if err != nil {
		log.Fatalf("load config: %v", err)
	}

	mux := http.NewServeMux()
	exec := newAgentExecutor(cfg)
	mux.HandleFunc("/command", newCommandHandler(cfg, exec))

	srv := &http.Server{
		Addr:              cfg.ListenAddr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}

	log.Printf("agent listening on %s", cfg.ListenAddr)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("server: %v", err)
	}
}

func isDynamicAllowed(cmd string, allowed []string) bool {
	for _, a := range allowed {
		if strings.EqualFold(cmd, a) {
			return true
		}
	}
	return false
}

type chatCWDStore struct {
	mu   sync.Mutex
	byID map[int64]string
}

func newChatCWD() *chatCWDStore {
	return &chatCWDStore{byID: make(map[int64]string)}
}

func (s *chatCWDStore) get(chatID int64, base string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if v, ok := s.byID[chatID]; ok {
		return v
	}
	s.byID[chatID] = base
	return base
}

func (s *chatCWDStore) set(chatID int64, dir string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.byID[chatID] = dir
}

func handleDynamicCommand(cfg *AgentConfig, store *chatCWDStore, chatID int64, cmd string, args []string) api.CommandResponse {
	base := strings.TrimSpace(cfg.Execution.BaseDir)
	if base == "" {
		return api.CommandResponse{Ok: false, ExitCode: 1, Error: "base_dir not configured"}
	}

	baseAbs, err := filepath.Abs(base)
	if err != nil {
		return api.CommandResponse{Ok: false, ExitCode: 1, Error: "invalid base_dir"}
	}

	switch strings.ToLower(cmd) {
	case "pwd":
		cwd := store.get(chatID, baseAbs)
		return api.CommandResponse{Ok: true, ExitCode: 0, Stdout: cwd + "\n"}
	case "ls", "ll":
		cwd := store.get(chatID, baseAbs)
		return runSafeList(baseAbs, cwd, cmd, args, cfg.Execution.DefaultTimeoutSec, cfg.Execution.MaxOutputKB)
	case "cat":
		cwd := store.get(chatID, baseAbs)
		return runSafeCat(baseAbs, cwd, args, cfg.Execution.DefaultTimeoutSec, cfg.Execution.MaxOutputKB)
	case "cd":
		return runSafeCd(baseAbs, store, chatID, args)
	default:
		return api.CommandResponse{Ok: false, ExitCode: 1, Error: "unsupported dynamic command"}
	}
}

func runSafeList(baseAbs, cwdAbs, cmd string, args []string, timeoutSec int, maxKB int) api.CommandResponse {
	flags := []string{}
	paths := []string{}

	if strings.ToLower(cmd) == "ll" {
		flags = append(flags, "-la")
	}

	for _, a := range args {
		if strings.HasPrefix(a, "-") {
			if !isAllowedLsFlag(a) {
				return api.CommandResponse{Ok: false, ExitCode: 1, Error: "ls flag not allowed: " + a}
			}
			flags = append(flags, a)
		} else {
			p, err := sanitizePath(baseAbs, cwdAbs, a)
			if err != nil {
				return api.CommandResponse{Ok: false, ExitCode: 1, Error: err.Error()}
			}
			paths = append(paths, p)
		}
	}

	if len(paths) == 0 {
		paths = []string{cwdAbs}
	}

	return runCommand(cwdAbs, "/bin/ls", append(flags, paths...), timeoutSec, maxKB)
}

func runSafeCat(baseAbs, cwdAbs string, args []string, timeoutSec int, maxKB int) api.CommandResponse {
	if len(args) == 0 {
		return api.CommandResponse{Ok: false, ExitCode: 1, Error: "cat requires a file path"}
	}
	paths := []string{}
	for _, a := range args {
		if strings.HasPrefix(a, "-") {
			return api.CommandResponse{Ok: false, ExitCode: 1, Error: "cat flags not allowed"}
		}
		p, err := sanitizePath(baseAbs, cwdAbs, a)
		if err != nil {
			return api.CommandResponse{Ok: false, ExitCode: 1, Error: err.Error()}
		}
		paths = append(paths, p)
	}
	return runCommand(baseAbs, "/bin/cat", paths, timeoutSec, maxKB)
}

func isAllowedLsFlag(flag string) bool {
	allowed := map[string]bool{
		"-a":  true,
		"-l":  true,
		"-h":  true,
		"-t":  true,
		"-r":  true,
		"-1":  true,
		"-la": true,
		"-al": true,
	}
	return allowed[flag]
}

func sanitizePath(baseAbs string, cwdAbs string, p string) (string, error) {
	if strings.TrimSpace(p) == "" {
		return "", fmt.Errorf("empty path")
	}
	var abs string
	if filepath.IsAbs(p) {
		abs = filepath.Clean(p)
	} else {
		abs = filepath.Clean(filepath.Join(cwdAbs, p))
	}

	rel, err := filepath.Rel(baseAbs, abs)
	if err != nil {
		return "", fmt.Errorf("invalid path")
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return "", fmt.Errorf("path outside base_dir")
	}

	if info, err := os.Lstat(abs); err == nil && info.Mode()&os.ModeSymlink != 0 {
		if eval, err := filepath.EvalSymlinks(abs); err == nil {
			if relEval, err := filepath.Rel(baseAbs, eval); err == nil {
				if relEval == ".." || strings.HasPrefix(relEval, ".."+string(os.PathSeparator)) {
					return "", fmt.Errorf("symlink points outside base_dir")
				}
			}
		}
	}

	return abs, nil
}

func runSafeCd(baseAbs string, store *chatCWDStore, chatID int64, args []string) api.CommandResponse {
	if len(args) == 0 {
		store.set(chatID, baseAbs)
		return api.CommandResponse{Ok: true, ExitCode: 0, Stdout: baseAbs + "\n"}
	}
	if len(args) > 1 {
		return api.CommandResponse{Ok: false, ExitCode: 1, Error: "cd accepts a single path"}
	}
	target, err := sanitizePath(baseAbs, store.get(chatID, baseAbs), args[0])
	if err != nil {
		return api.CommandResponse{Ok: false, ExitCode: 1, Error: err.Error()}
	}
	info, err := os.Stat(target)
	if err != nil || !info.IsDir() {
		return api.CommandResponse{Ok: false, ExitCode: 1, Error: "not a directory"}
	}
	store.set(chatID, target)
	return api.CommandResponse{Ok: true, ExitCode: 0, Stdout: target + "\n"}
}

func runCommand(baseAbs, execPath string, args []string, timeoutSec int, maxKB int) api.CommandResponse {
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(timeoutSec)*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, execPath, args...)
	cmd.Dir = baseAbs
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	resp := api.CommandResponse{}
	if err == nil {
		resp.Ok = true
		resp.ExitCode = 0
	} else {
		resp.Ok = false
		resp.ExitCode = exitCode(err)
		resp.Error = err.Error()
	}
	resp.Stdout = limitOutput(stdout.String(), maxKB)
	resp.Stderr = limitOutput(stderr.String(), maxKB)
	return resp
}

func runAllowedCommand(ctx context.Context, allowed api.AllowedCommand, maxKB int) api.CommandResponse {
	cmd := exec.CommandContext(ctx, allowed.Exec, allowed.Args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	resp := api.CommandResponse{}
	if err == nil {
		resp.Ok = true
		resp.ExitCode = 0
	} else {
		resp.Ok = false
		resp.ExitCode = exitCode(err)
		resp.Error = err.Error()
	}
	resp.Stdout = limitOutput(stdout.String(), maxKB)
	resp.Stderr = limitOutput(stderr.String(), maxKB)
	return resp
}

func exitCode(err error) int {
	var exitErr *exec.ExitError
	if err == context.DeadlineExceeded {
		return 124
	}
	if ok := strings.Contains(err.Error(), "signal: killed"); ok {
		return 137
	}
	if err == nil {
		return 0
	}
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode()
	}
	return 1
}

func limitOutput(s string, maxKB int) string {
	maxBytes := maxKB * 1024
	if len(s) <= maxBytes {
		return s
	}
	return s[:maxBytes] + "\n[truncated]\n"
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	if err := enc.Encode(v); err != nil {
		fmt.Fprintf(os.Stderr, "encode response: %v\n", err)
	}
}
