package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"personal_ai/internal/api"
)

type localExecutor struct {
	cfg     *BrokerConfig
	chatCWD *chatCWDStore
}

func newLocalExecutor(cfg *BrokerConfig) *localExecutor {
	return &localExecutor{cfg: cfg, chatCWD: newChatCWD()}
}

func (e *localExecutor) Execute(ctx context.Context, req api.CommandRequest) (*api.CommandResponse, error) {
	cmdName := strings.TrimSpace(req.Command)
	if cmdName == "" {
		resp := api.CommandResponse{Ok: false, ExitCode: 1, Error: "empty command"}
		return &resp, nil
	}

	if isDynamicAllowed(cmdName, e.cfg.Execution.Local.DynamicAllowlist) {
		resp := handleDynamicCommand(e.cfg, e.chatCWD, req.ChatID, cmdName, req.Args)
		return &resp, nil
	}

	allowed, ok := e.cfg.Execution.Local.CommandAllowlist[cmdName]
	if !ok {
		resp := api.CommandResponse{Ok: false, ExitCode: 1, Error: "command not allowed"}
		return &resp, nil
	}

	ctx, cancel := context.WithTimeout(ctx, time.Duration(e.cfg.Execution.Local.DefaultTimeoutSec)*time.Second)
	defer cancel()

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
	resp.Stdout = limitOutput(stdout.String(), e.cfg.Execution.Local.MaxOutputKB)
	resp.Stderr = limitOutput(stderr.String(), e.cfg.Execution.Local.MaxOutputKB)
	return &resp, nil
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

func handleDynamicCommand(cfg *BrokerConfig, store *chatCWDStore, chatID int64, cmd string, args []string) api.CommandResponse {
	base := strings.TrimSpace(cfg.Execution.Local.BaseDir)
	if base == "" {
		return api.CommandResponse{Ok: false, ExitCode: 1, Error: "execution.local.base_dir not configured"}
	}

	baseAbs, err := filepath.Abs(base)
	if err != nil {
		return api.CommandResponse{Ok: false, ExitCode: 1, Error: "invalid execution.local.base_dir"}
	}

	switch strings.ToLower(cmd) {
	case "pwd":
		cwd := store.get(chatID, baseAbs)
		return api.CommandResponse{Ok: true, ExitCode: 0, Stdout: cwd + "\n"}
	case "ls", "ll":
		cwd := store.get(chatID, baseAbs)
		return runSafeList(baseAbs, cwd, cmd, args, cfg.Execution.Local.DefaultTimeoutSec, cfg.Execution.Local.MaxOutputKB)
	case "cat":
		cwd := store.get(chatID, baseAbs)
		return runSafeCat(baseAbs, cwd, args, cfg.Execution.Local.DefaultTimeoutSec, cfg.Execution.Local.MaxOutputKB)
	case "cd":
		return runSafeCd(baseAbs, store, chatID, args)
	case "touch":
		cwd := store.get(chatID, baseAbs)
		return runSafeTouch(baseAbs, cwd, args)
	case "mkdir":
		cwd := store.get(chatID, baseAbs)
		return runSafeMkdir(baseAbs, cwd, args)
	case "write":
		cwd := store.get(chatID, baseAbs)
		return runSafeWrite(baseAbs, cwd, args, false)
	case "append":
		cwd := store.get(chatID, baseAbs)
		return runSafeWrite(baseAbs, cwd, args, true)
	case "count":
		cwd := store.get(chatID, baseAbs)
		return runSafeCount(baseAbs, cwd, args)
	case "find":
		cwd := store.get(chatID, baseAbs)
		return runSafeFind(baseAbs, cwd, args)
	case "ping":
		return runSafePing(args)
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

func runSafeTouch(baseAbs, cwdAbs string, args []string) api.CommandResponse {
	if len(args) != 1 {
		return api.CommandResponse{Ok: false, ExitCode: 1, Error: "touch requires a single file path"}
	}
	target, err := sanitizePath(baseAbs, cwdAbs, args[0])
	if err != nil {
		return api.CommandResponse{Ok: false, ExitCode: 1, Error: err.Error()}
	}
	f, err := os.OpenFile(target, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return api.CommandResponse{Ok: false, ExitCode: 1, Error: err.Error()}
	}
	_ = f.Close()
	return api.CommandResponse{Ok: true, ExitCode: 0, Stdout: target + "\n"}
}

func runSafeMkdir(baseAbs, cwdAbs string, args []string) api.CommandResponse {
	if len(args) != 1 {
		return api.CommandResponse{Ok: false, ExitCode: 1, Error: "mkdir requires a single directory path"}
	}
	target, err := sanitizePath(baseAbs, cwdAbs, args[0])
	if err != nil {
		return api.CommandResponse{Ok: false, ExitCode: 1, Error: err.Error()}
	}
	if err := os.MkdirAll(target, 0o755); err != nil {
		return api.CommandResponse{Ok: false, ExitCode: 1, Error: err.Error()}
	}
	return api.CommandResponse{Ok: true, ExitCode: 0, Stdout: target + "\n"}
}

func runSafeWrite(baseAbs, cwdAbs string, args []string, appendMode bool) api.CommandResponse {
	if len(args) < 2 {
		return api.CommandResponse{Ok: false, ExitCode: 1, Error: "write requires a file path and content"}
	}
	target, err := sanitizePath(baseAbs, cwdAbs, args[0])
	if err != nil {
		return api.CommandResponse{Ok: false, ExitCode: 1, Error: err.Error()}
	}
	content := strings.Join(args[1:], " ")
	if len(content) > 32*1024 {
		return api.CommandResponse{Ok: false, ExitCode: 1, Error: "content too large"}
	}
	if appendMode {
		f, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
		if err != nil {
			return api.CommandResponse{Ok: false, ExitCode: 1, Error: err.Error()}
		}
		_, err = f.WriteString(content)
		_ = f.Close()
		if err != nil {
			return api.CommandResponse{Ok: false, ExitCode: 1, Error: err.Error()}
		}
		return api.CommandResponse{Ok: true, ExitCode: 0, Stdout: target + "\n"}
	}
	if err := os.WriteFile(target, []byte(content), 0o644); err != nil {
		return api.CommandResponse{Ok: false, ExitCode: 1, Error: err.Error()}
	}
	return api.CommandResponse{Ok: true, ExitCode: 0, Stdout: target + "\n"}
}

func runSafeCount(baseAbs, cwdAbs string, args []string) api.CommandResponse {
	target := cwdAbs
	if len(args) > 1 {
		return api.CommandResponse{Ok: false, ExitCode: 1, Error: "count accepts at most one path"}
	}
	if len(args) == 1 {
		p, err := sanitizePath(baseAbs, cwdAbs, args[0])
		if err != nil {
			return api.CommandResponse{Ok: false, ExitCode: 1, Error: err.Error()}
		}
		target = p
	}
	info, err := os.Stat(target)
	if err != nil {
		return api.CommandResponse{Ok: false, ExitCode: 1, Error: err.Error()}
	}
	if !info.IsDir() {
		return api.CommandResponse{Ok: true, ExitCode: 0, Stdout: "1\n"}
	}
	entries, err := os.ReadDir(target)
	if err != nil {
		return api.CommandResponse{Ok: false, ExitCode: 1, Error: err.Error()}
	}
	count := 0
	for _, e := range entries {
		if e.Type().IsRegular() {
			count++
		}
	}
	return api.CommandResponse{Ok: true, ExitCode: 0, Stdout: fmt.Sprintf("%d\n", count)}
}

func runSafeFind(baseAbs, cwdAbs string, args []string) api.CommandResponse {
	if len(args) != 1 {
		return api.CommandResponse{Ok: false, ExitCode: 1, Error: "find requires a single name fragment"}
	}
	needle := strings.ToLower(strings.TrimSpace(args[0]))
	if needle == "" {
		return api.CommandResponse{Ok: false, ExitCode: 1, Error: "find requires a non-empty name fragment"}
	}

	const maxDepth = 7
	const maxResults = 200
	results := []string{}

	baseAbsClean := baseAbs
	err := filepath.WalkDir(cwdAbs, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(baseAbsClean, path)
		if err != nil {
			return err
		}
		depth := 0
		if rel != "." {
			depth = strings.Count(rel, string(os.PathSeparator))
		}
		if depth > maxDepth {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			name := strings.ToLower(d.Name())
			if strings.Contains(name, needle) {
				results = append(results, path)
				if len(results) >= maxResults {
					return filepath.SkipDir
				}
			}
		}
		return nil
	})
	if err != nil {
		return api.CommandResponse{Ok: false, ExitCode: 1, Error: err.Error()}
	}
	if len(results) == 0 {
		return api.CommandResponse{Ok: true, ExitCode: 0, Stdout: "(no matches)\n"}
	}
	return api.CommandResponse{Ok: true, ExitCode: 0, Stdout: strings.Join(results, "\n") + "\n"}
}

func runSafePing(args []string) api.CommandResponse {
	if len(args) != 1 {
		return api.CommandResponse{Ok: false, ExitCode: 1, Error: "ping requires a single host"}
	}
	host := strings.TrimSpace(args[0])
	if host == "" {
		return api.CommandResponse{Ok: false, ExitCode: 1, Error: "ping requires a non-empty host"}
	}
	if !isSafeHost(host) {
		return api.CommandResponse{Ok: false, ExitCode: 1, Error: "ping host not allowed"}
	}
	return runCommand(".", "/bin/ping", []string{"-c", "4", "-W", "2", host}, 10, 8)
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

func isSafeHost(host string) bool {
	if len(host) > 253 {
		return false
	}
	for _, r := range host {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '.' || r == '-' {
			continue
		}
		return false
	}
	if strings.HasPrefix(host, "-") || strings.HasSuffix(host, "-") || strings.HasPrefix(host, ".") || strings.HasSuffix(host, ".") {
		return false
	}
	return true
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
