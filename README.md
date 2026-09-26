# go-tangra-ticket

Tenant email helpdesk service for the
[go-tangra v4 platform](https://github.com/go-tangra/go-tangra).

It keeps **tickets** (status, priority, assignee, tags) with a **conversation
timeline** of internal notes and emailed public replies. **Inbound email** arrives
on a separate off-mesh edge and becomes a new ticket or threads back into an
existing one (`In-Reply-To`/`References` or the `[#<ticket-id>]` reference token).
Loop-safe (RFC 3834) auto-acknowledgements, sandboxed **CEL triage rules**,
mailboxes, history, statistics and tenant backup come with it. Attachments live in
S3-compatible object storage (RustFS or MinIO), HTML bodies are only ever returned
sanitised, and the relay token and SMTP password are secret **references**
(warden in production) that are resolved at use time and never logged.
Changes are published live on the platform event bus.

Operations: [`deploy/README.md`](deploy/README.md).
Design history: `specs/014-ticket-service`.

## Place in the platform

```
go-tangra/go-tangra          platform module + @go-tangra/ui kit
        |
go-tangra-auth  <---->  go-tangra-portal (gateway)  <---->  go-tangra-lcm
                                  |
mail relay --(:9957)-->  go-tangra-ticket  ---->  object storage (S3), SMTP relay
                                  |
                          go-tangra-warden (relay token, SMTP password)
```

- Built on `github.com/go-tangra/go-tangra/v4` (mTLS transports, identity,
  service policy, audit, observability).
- Verifies platform tokens, resolves agents and checks permissions through the
  auth SDK (`github.com/go-tangra/go-tangra-auth/sdk/v4`).
- Registers with the gateway through the portal SDK
  (`github.com/go-tangra/go-tangra-portal/sdk/v4`), which fronts the browser API
  (`/api/ticket`) and the federated UI remote.
- Enrolls for its SVID with lcm over the network
  (`github.com/go-tangra/go-tangra-lcm/sdk/v4`), as the platform stack does.
- Resolves `warden:` secret references through the warden SDK
  (`github.com/go-tangra/go-tangra-warden/sdk/v4`).

The repository holds one Go module, `github.com/go-tangra/go-tangra-ticket/v4`.
Other services call it through `pkg/ticketclient` and the `ticket.v1` protos
(not proxied by the gateway).

## Layout

| Path | What |
|------|------|
| `api/openapi/ticket.yaml` | browser API contract (served under `/api/ticket`) |
| `api/proto/ticket/v1/` | module gRPC surface (`Tickets/{Create,Get,List,AddComment}`) |
| `internal/config` | configuration + validation (secure defaults, named opt-outs) |
| `internal/store`, `internal/repo` | TimescaleDB schema (RLS), repositories; `internal/memstore` is the in-memory test double |
| `internal/inbound` | off-mesh inbound mail edge (relay token, routing by mailbox address) |
| `internal/mailparse`, `internal/sanitize`, `internal/thread` | RFC 822 parsing, HTML sanitising, threading |
| `internal/mailer` | outbound SMTP (replies, acknowledgements) |
| `internal/secrets` | `warden:` / `file:` secret references with refresh |
| `internal/rules` | CEL triage rules (compile on save, cost limit, deadline) |
| `internal/blob` | S3-compatible attachment storage (minio-go) |
| `internal/sealed` | envelope encryption with the KEK |
| `internal/authz`, `internal/agents` | permission checks, assignable agents from auth |
| `internal/tickets`, `internal/comments`, `internal/tags`, `internal/mailboxes`, `internal/history` | domain services |
| `internal/events`, `internal/stream` | events on the platform bus, live stream |
| `internal/backup`, `internal/stats`, `internal/audit`, `internal/metrics` | backup export/import, statistics, audit, metrics |
| `internal/httpapi`, `internal/grpcapi` | browser and service APIs |
| `internal/app`, `cmd/ticketsvc` | wiring and the service binary (serve, `bootstrap`, `version`) |
| `pkg/ticketmanifest` | gateway manifest, module roles and built-in role grants |
| `pkg/ticketclient` | Go client other services use |
| `testdata/mail` | `.eml` fixture corpus (parser, sanitiser, threading, loop safety) |
| `deploy` | service policy and operations notes |
| `ui/` | Vue 3 + FlyonUI federated remote on `@go-tangra/ui` |

## Build and test

You need Go 1.26, Node 22, Docker (for the integration suite and the image), and a
GitHub token with `read:packages` to install `@go-tangra/ui` from GitHub Packages.

```bash
go build ./... && go vet ./... && go test -race ./...
buf lint
make test-integration                     # -tags integration: TimescaleDB, Valkey, RustFS, Mailpit via testcontainers
make lint cover vuln

cd ui
export NODE_AUTH_TOKEN=$(gh auth token)   # ui/.npmrc only references this variable
npm ci && npm run lint && npm run test:unit && npm run build
```

The integration suite (`tests/integration`) drives the whole email loop: a
fixture in, the ticket and its attachment stored, the acknowledgement and an
agent reply in Mailpit, and the requester's answer threaded back. Each
dependency can also point at a running service (`TICKET_IT_*` variables, see the
package comment).

The unit coverage gate requires at least 80 % overall and 100 % for the
authorization, sealing, secret-resolution and threading packages. Generated code,
SQL bindings and wiring are covered by the integration suite instead. The
Playwright specs in `ui/tests/e2e` need a running platform and operator
credentials; they skip otherwise.

## Run

The service runs in the go-tangra platform stack (`deploy/stack` in
[go-tangra](https://github.com/go-tangra/go-tangra)), next to TimescaleDB, Valkey,
RustFS and Mailpit. The stack mounts its configuration at
`/app/deploy/container.yaml`, the development key-encryption key at
`/app/deploy/kek.dev`, the edge certificate at `/edge` and a generated
development relay token at `/secrets/relay.token`. `deploy/kek.dev` in this
repository is a development key only; it is excluded from the image.

```bash
ticketsvc bootstrap -config deploy/container.yaml    # apply migrations and exit
ticketsvc -config deploy/container.yaml              # serve (applies migrations)
```

Listeners: gRPC `:9955` and HTTP `:9956` on the mesh (mTLS), admin `:9840`
(`/healthz`, `/readyz`), and the inbound mail edge `:9957` (TLS, relay token).
See `specs/014-ticket-service/quickstart.md` for the end-to-end walkthrough.

## Container image

The image is `ghcr.io/go-tangra/go-tangra-ticket`, built by
`.github/workflows/ci.yaml`. It carries `ticketsvc` with the embedded UI remote.

```bash
docker buildx build --secret id=npm_token,env=NODE_AUTH_TOKEN \
  --build-arg APP_VERSION=4.0.0 -t go-tangra-ticket:dev .
docker run --rm go-tangra-ticket:dev version
```

The image runs `ticketsvc -config deploy/container.yaml` as user `app`
(uid 10001). It contains no configuration and no key material: deployments mount
their own `deploy/container.yaml`, key-encryption key, inbound edge certificate
and secret references. The object store and the SMTP relay are separate services
the configuration points at (`object_store.endpoint`, `smtp.host`); the bucket is
created on start when missing.

## API permissions

`tickets:read/manage/delete`, `tags:manage`, `mailboxes:manage`, `rules:manage`,
`stats:read`, `backup:manage`. The gateway enforces the per-route permission from
the manifest; the module then checks the tenant scope.

## Roles

The module registers its permissions with auth at start and every five
minutes, together with ready-made module roles that auth offers in every
tenant (locked; administrators assign them or clone them into custom roles):

| Role | Display name | Permissions |
|---|---|---|
| `administrator` | Tickets administrator | all 8 |
| `agent` | Tickets agent | `tickets:read`, `tickets:manage`, `tags:manage` |
| `viewer` | Tickets viewer | `tickets:read` |

Built-in role grants (scoped to the ticket module by auth,
`pkg/ticketmanifest.Grants`): `owner` and `admin` hold the administrator set,
`operator` the agent set, `member` and `auditor` the viewer set.

## Versioning

- Releases are tagged `vX.Y.Z`. CI publishes the image as `X.Y.Z`, `X.Y`, `X`
  and `sha-<short>`. There is no `latest` tag.
- v4.0.0 rebuilds the service on the go-tangra v4 platform. The previous line
  stays on the `v3` branch and its `v1.x` tags.
