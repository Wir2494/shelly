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
