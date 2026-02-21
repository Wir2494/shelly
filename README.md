# Telegram Broker + Home Agent

This repo contains two small Go services:
- `broker`: talks to Telegram, authorizes, rate-limits, and either executes allowlisted commands locally or forwards them to a local agent.
- `agent`: runs on your machine, executes allowlisted commands and returns output.

## Architecture
Flow:
- Telegram -> Broker (polling)
- Broker -> LLM (if `llm.enabled` is true)
- Broker -> local execution (local mode)
- OR Broker -> Agent (forward mode)
- Agent executes allowlisted commands only
- Broker -> Telegram (response)

Diagram:
```
Telegram
   |
   v
Broker (polling, auth, rate-limit)
   | \
   |  \----> LLM (if llm.enabled)
   |            |
   |            v
   |        decision: chat or command
   |
   +--> local exec (local mode)
   |
   +--> Agent (forward mode) -> allowed command -> stdout/stderr
   |
   v
Telegram response
```

## Instructions
Prereqs:
- Go installed (builds the `broker` and `agent` binaries)
- Telegram bot token and your Telegram user ID
- OpenAI API key if `llm.enabled` is true

1. Create local configs from the examples:
```
cp configs/broker.example.json configs/broker.json
cp configs/agent.example.json configs/agent.json
```

2. Fill in `configs/broker.json`:
- `telegram.bot_token`: your bot token
- `telegram.allowed_user_ids`: your user ID(s)
- `telegram.mode`: set to `polling`
- `execution.mode`: `local` or `forward`
- `execution.forward_url`: required if `execution.mode` is `forward` (e.g. `http://127.0.0.1:8081/command`)
- `execution.forward_auth_token`: shared secret between broker and agent (forward mode)
- `execution.local.base_dir`, `execution.local.dynamic_allowlist`, `execution.local.command_allowlist`: required for local mode
- `llm.enabled`: set to `true` or `false`
- `llm.api_key`, `llm.model`: required when `llm.enabled` is `true`

3. Fill in `configs/agent.json` (only if using `execution.mode: "forward"`):
- `auth_token`: must match `execution.forward_auth_token`
- `execution.base_dir`: base directory for dynamic commands
- `execution.dynamic_allowlist`: allowed dynamic commands
- `execution.command_allowlist`: allowed static commands

4. Build binaries:
```
go build -o broker ./cmd/broker
go build -o agent ./cmd/agent
```

5. Run the services (same machine):
- Broker:
```
./broker -config configs/broker.json
```
- Agent (only if `execution.mode` is `forward`):
```
./agent -config configs/agent.json
```

## Dynamic Commands (Scoped to a Base Directory)
The local executor (or agent) supports safe, scoped filesystem commands under `base_dir`:

- `pwd` (returns current per-chat directory)
- `ls`, `ll` (subset of flags allowed)
- `cat <file>`
- `cd <dir>` (per-chat working directory)

Configure in `configs/agent.json`:
- `execution.base_dir`: e.g. `/home/wir`
- `execution.dynamic_allowlist`: e.g. `["ls","ll","cat","pwd","cd"]`

All paths are constrained to `base_dir`. Paths outside it are rejected.

## Security Model
The system is allowlist-first. The broker authorizes Telegram users by ID, enforces per-user rate limits, and only accepts commands present in the allowlist while denying any in the blocklist. When running in forward mode, the broker and agent authenticate with a shared `X-Auth-Token`. Dynamic commands are constrained to a configured base directory and sanitized to prevent path escapes.

## LLM Command Routing
The broker can map natural language into allowed commands using an LLM.
When enabled, the broker sends the user text to the OpenAI Responses API and expects a
JSON schema response that classifies the message as either:
- `command` with `intent` and `args`
- `chat` with a `response`

If the returned `confidence` is below `llm.confidence_threshold`, the broker will ask
the user to rephrase or use a direct command.

Configure in `configs/broker.json`:
- `llm.enabled`: set to `true`
- `llm.api_key`: your OpenAI API key
- `llm.model`: model name (default `gpt-5.2`)
- `llm.timeout_sec`: request timeout (default `15`)
- `llm.confidence_threshold`: minimum confidence (default `0.7`)

Notes:
- LLM routing only maps to the existing `command_allowlist`.
- If LLM fails or returns invalid JSON, the broker replies with an error.

## Example Telegram Commands
```
status
disk
memory
users
pwd
ls
ll Projects
cd Projects
pwd
cat /home/wir/Projects/personal_ai/README.md
```
