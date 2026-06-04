# go-tangra-ticket

Support ticket system for the go-tangra platform.

- **Email-to-ticket**: inbound mail forwarded by [iris](https://github.com/menta2k/iris)/KumoMTA
  (`POST /webhooks/iris`, raw RFC822) becomes a ticket — deduped by Message-Id.
- **Assignable**: every ticket has an `assignee_id`; `ListAssignableUsers`
  proxies admin-service to populate the assignee picker.
- **Lifecycle**: open → in_progress → pending → resolved → closed, with priority.
- **Comments**: public replies and internal notes per ticket.

gRPC `:10800` (admin gateway, mTLS) · HTTP `:10801` (iris webhook + frontend) ·
metrics `:10810`.

See [CLAUDE.md](./CLAUDE.md) for architecture, env vars, and codegen.

## Quick start

```bash
make generate          # buf + ent + wire
make build-server
make run-server        # needs PostgreSQL (db "ticket") + the platform
```
