package api

type AllowedCommand struct {
	Exec string   `json:"exec"`
	Args []string `json:"args"`
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
