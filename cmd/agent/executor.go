package main

import (
	"context"
	"strings"
	"time"

	"personal_ai/internal/api"
)

type CommandExecutor interface {
	Execute(ctx context.Context, req api.CommandRequest) api.CommandResponse
}

type agentExecutor struct {
	cfg     *AgentConfig
	chatCWD *chatCWDStore
}

func newAgentExecutor(cfg *AgentConfig) *agentExecutor {
	return &agentExecutor{cfg: cfg, chatCWD: newChatCWD()}
}

func (e *agentExecutor) Execute(ctx context.Context, req api.CommandRequest) api.CommandResponse {
	cmdName := strings.TrimSpace(req.Command)
	if cmdName == "" {
		return api.CommandResponse{Ok: false, ExitCode: 1, Error: "empty command"}
	}
	if isBlocked(cmdName, e.cfg.Execution.CommandBlocklist) {
		return api.CommandResponse{Ok: false, ExitCode: 1, Error: "command blocked"}
	}

	if isDynamicAllowed(cmdName, e.cfg.Execution.DynamicAllowlist) {
		return handleDynamicCommand(e.cfg, e.chatCWD, req.ChatID, cmdName, req.Args)
	}

	allowed, ok := e.cfg.Execution.CommandAllowlist[cmdName]
	if !ok {
		return api.CommandResponse{Ok: false, ExitCode: 1, Error: "command not allowed"}
	}

	execCtx, cancel := context.WithTimeout(ctx, time.Duration(e.cfg.Execution.DefaultTimeoutSec)*time.Second)
	defer cancel()

	return runAllowedCommand(execCtx, allowed, e.cfg.Execution.MaxOutputKB)
}
