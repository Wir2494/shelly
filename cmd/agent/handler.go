package main

import (
	"encoding/json"
	"io"
	"net/http"

	"personal_ai/internal/api"
)

func newCommandHandler(cfg *AgentConfig, exec CommandExecutor) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		if cfg.AuthToken != "" {
			if r.Header.Get("X-Auth-Token") != cfg.AuthToken {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
		}
		body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		var req api.CommandRequest
		if err := json.Unmarshal(body, &req); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}

		resp := exec.Execute(r.Context(), req)
		status := http.StatusOK
		if !resp.Ok {
			switch resp.Error {
			case "empty command":
				status = http.StatusBadRequest
			case "command blocked", "command not allowed":
				status = http.StatusForbidden
			}
		}
		writeJSON(w, status, resp)
	}
}
