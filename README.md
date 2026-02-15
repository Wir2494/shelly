# Telegram Broker + Home Agent (VPS Middle-Man)

This repo contains two small Go services:
- `broker`: runs on your VPS, receives Telegram webhooks, authorizes, rate-limits, and forwards allowlisted commands to your home agent.
- `agent`: runs on your home Ubuntu server, executes allowlisted commands and returns output.

## Quick Architecture
- Telegram -> VPS Broker (HTTPS webhook or polling)
- Broker -> Home Agent via reverse SSH tunnel (VPS loopback -> home loopback)
- Agent executes allowlisted commands only

## Build
```bash
go build -o broker ./cmd/broker
go build -o agent ./cmd/agent
```

## Config

Create local configs from the examples:
```
cp configs/broker.example.json configs/broker.json
cp configs/agent.example.json configs/agent.json
```

Then edit:
- `configs/broker.json`
  - `telegram_bot_token`
  - `telegram_allowed_user_ids`
  - `forward_auth_token`
- `configs/agent.json`
  - `auth_token` (must match `forward_auth_token`)
  - `base_dir`

## Polling Mode (No TLS/Domain Needed)
If you do not have a domain with TLS, set:
- `configs/broker.json` -> `telegram_mode` = `polling`

In polling mode, the VPS broker calls Telegram `getUpdates` directly. No inbound HTTPS is required.

## Dynamic Commands (Scoped to a Base Directory)
The agent supports safe, scoped filesystem commands under `base_dir`:

- `pwd` (returns current per-chat directory)
- `ls`, `ll` (subset of flags allowed)
- `cat <file>`
- `cd <dir>` (per-chat working directory)

Configure in `configs/agent.json`:
- `base_dir`: e.g. `/home/wir`
- `dynamic_allowlist`: e.g. `["ls","ll","cat","pwd","cd"]`

All paths are constrained to `base_dir`. Paths outside it are rejected.

## Systemd (examples)
- VPS: `systemd/broker.service` (configured for `/home/wir/broker`)
- Home server: `systemd/agent.service`
- Home server SSH tunnel: `systemd/ssh-tunnel.service.example` (copy to `/etc/systemd/system/ssh-tunnel.service` and fill in your host/user)

Adjust:
- `USER@VPS_HOST` (in `systemd/ssh-tunnel.service`)
- install paths (`/home/wir/broker` on VPS, `/opt/personal_ai` on home server)
- service users as desired

## Reverse SSH Tunnel
Home server opens:
```
ssh -N -R 127.0.0.1:18080:127.0.0.1:8080 USER@VPS_HOST
```
The broker calls `http://127.0.0.1:18080/command` on the VPS.

Note: This repo config uses the agent on `127.0.0.1:8081`. The tunnel service reflects that.

## Telegram Webhook vs Polling
If you do not have a domain with TLS, use polling:
- Set `telegram_mode` to `polling` in `configs/broker.json`.
- No webhook required.

If you have a domain with TLS, you can use webhooks:
```
https://YOUR_VPS_DOMAIN/telegram/webhook
```
The broker listens on `telegram_webhook_path` and should be behind TLS (Caddy/Nginx).

## Security Model
- Allowlist only.
- Blocklist enforced in both broker and agent.
- Auth token between broker and agent (`X-Auth-Token`).
- Rate limits on broker.

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
