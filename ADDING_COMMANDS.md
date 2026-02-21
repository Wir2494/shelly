# Adding Allowed Commands

This guide explains how to add new commands to the system safely and consistently.

## Decide the Command Type

- **Dynamic command (path-scoped)**: User-supplied args, constrained to `execution.base_dir`.
  - Examples: `ls`, `cd`, `cat`, `touch`, `mkdir`, `count`, `find`, `ping`
- **Static allowlist command**: Fixed exec path + args (no user-supplied args).
  - Examples: `status`, `disk`, `date`, `nano`

## Update Configs

### Broker (`configs/broker.json`)
Add the command to:
- `policy.command_allowlist`

### Agent (`configs/agent.json`)
Depending on type:
- **Dynamic**: add to `execution.dynamic_allowlist`
- **Static**: add under `execution.command_allowlist` with exec path + args

## Code Changes (Only for New Dynamic Commands)

If you are adding a **new dynamic command**, you must implement it in code:

1. Add a handler in:
   - `cmd/broker/local_exec.go` (`handleDynamicCommand`)
   - `cmd/agent/main.go` (`handleDynamicCommand`)
2. Add a safe implementation (path validation, limits).
3. Add tests:
   - `cmd/broker/dynamic_exec_test.go`
   - `cmd/agent/dynamic_exec_test.go`

## Rebuild and Restart

If code changed:
```bash
go build -o broker ./cmd/broker
go build -o agent ./cmd/agent
```

Always restart services to pick up config changes:
```bash
sudo systemctl restart broker
sudo systemctl restart agent
```

## Quick Checklist

- [ ] Added to broker allowlist
- [ ] Added to agent dynamic or static allowlist
- [ ] Implemented code for new dynamic commands
- [ ] Tests updated and passing
- [ ] Rebuilt binaries (if code changed)
- [ ] Services restarted
