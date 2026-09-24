# Tasks: Ticket Service

**Feature**: 014-ticket-service | **Spec**: [spec.md](./spec.md) | **Plan**: [plan.md](./plan.md)

Organized by phase; user-story phases are independently testable. Tests are
MANDATORY and precede implementation (Constitution IV). `[P]` = parallelizable
(different files, no dependency on an incomplete task). Module path:
`github.com/go-freya/freya/services/ticket`. Mirror services/asset (module +
object storage + userdir) and services/inventory (off-mesh edge listener); reuse
services/ipam/internal/warden (secret references) and the notification module's
email channel (header-safe SMTP). The UI is an `@freya/ui` Module-Federation
remote mirroring services/ipam/ui (drawer pattern, `breakpointSpecificity`).
Reference implementation for behaviour: /home/jadmin/projects/go-tangra/go-tangra-ticket
(internal/webhook, internal/thread, internal/rules, internal/mailer, frontend/src).

## Phase 1: Setup (Shared Infrastructure)

- [X] T001 Create the module skeleton `services/ticket/` per plan.md: cmd/ticketsvc, api/{openapi,proto/ticket/v1}, internal/{app,config,store,repo,memstore,sealed,authz,audit,blob,mailparse,thread,rules,sanitize,mailer,agents,secrets,inbound,tickets,comments,tags,mailboxes,history,stats,backup,events,stream,metrics,httpapi,grpcapi}, pkg/{ticketmanifest,ticketclient}, testdata/mail, ui, deploy.
- [X] T002 Add `services/ticket/go.mod` (module .../services/ticket, Go 1.26) with replaces for ../.. ../auth ../gateway ../lcm ../warden; require minio-go/v7, golang.org/x/text, github.com/google/cel-go, github.com/microcosm-cc/bluemonday; seed go.sum from services/asset and `go mod tidy`.
- [X] T003 [P] Add `services/ticket/buf.yaml`, `buf.gen.yaml` and `api/proto/ticket/v1/ticket.proto` (Tickets service: Create/Get/List/AddComment per contracts §C) + Makefile `generate` target (mirror services/asset).
- [X] T004 [P] Add `services/ticket/Dockerfile` (build UI via the workspace, embed with -tags ui, build ticketsvc) and `Makefile` (test/cover/vuln/generate/build/image) mirroring services/asset.
- [X] T005 [P] Scaffold `services/ticket/ui/` from services/ipam/ui: package.json (name freya-ticket-ui, @freya/ui, zod), vite.config.ts (base /m/ticket/, tailwindcss + breakpointSpecificity + federation), module-federation.config.ts (remote `ticket`, host-only @freya/ui singletons), src/{main.ts,main.css,dev.css,api/client.ts (BASE /api/ticket/v1),remote/{routes.ts,nav.ts}}, eslint/tsconfig, playwright.config.ts, tests/unit/setup.ts, embed.go/embed_stub.go.
- [X] T006 [P] Build the `.eml` fixture corpus `services/ticket/testdata/mail/`: plain.eml, html-inline-image.eml (cid image), attachments.eml (pdf + png), legacy-charset.eml (windows-1251 + ISO-2022-JP subject), quoted-printable.eml, reply-in-reply-to.eml, reply-token-only.eml, auto-submitted.eml, bulk-precedence.eml, noreply-sender.eml, spam-high-score.eml, hostile-html.eml (script, onerror, remote img, form, css url), oversized-attachment.eml (generated in test helper), header-injection.eml.

## Phase 2: Foundational (Blocking Prerequisites)

- [X] T007 Typed, validated config `services/ticket/internal/config/config.go` (+ `config_test.go`): sections db, valkey, kek, object_store (endpoint/bucket/region/access_key/secret_key sealed/presign_ttl), inbound (addr, tls cert/key or insecure-dev, relay_token_ref, max_body_bytes 10MiB, max_attachment_bytes 25MiB, max_parts 100, max_depth 10), smtp (host, port, tls implicit|starttls|none, allow_plaintext, username, password_ref, mail_domain), rules (cost_limit, eval_timeout), events, gateway, mesh_enroll, limits_ticket; Default/Load/Validate (refuse start without relay_token_ref and KEK)/Warnings.
- [X] T008 Store migrations `services/ticket/internal/store/migrations/`: 0001_schema.sql (ticket_tickets, ticket_comments, ticket_attachments, ticket_tags, ticket_tag_links, ticket_rules, ticket_mailboxes, ticket_history per data-model.md with ALL unique/partial-unique constraints and indexes), 0002_audit.sql (ticket_audit_events hypertable), 0003_rls.sql (per-tenant RLS on every ticket_* table + ticket_app grants + security-definer `ticket_route_mailbox(address)` returning tenant_id, mailbox id, display_name, active, auto_ack, auto_ack_template only).
- [X] T009 Store models + repository interface `services/ticket/internal/store/models.go` and `services/ticket/internal/repo/repo.go`: tickets (create/get/list with filters status/priority/assignee incl. none/tag/query + paging + total, update fields, set assignee, set status with resolved_at, delete returning attachment keys), comments (create/list/delete, find by message id, last message id), attachments (create/list/get/delete-by-ticket), tags (CRUD, EnsureByName(kind,name), set ticket tags, list for tickets), rules (CRUD, list enabled ordered), mailboxes (CRUD, RouteMailbox system lookup), history (append/list), stats aggregates, backup iteration, audit append.
- [X] T010 [P] In-memory repository `services/ticket/internal/memstore/memstore.go` implementing repo.Store (filters, paging, partial-unique conflicts on external_id/message_id/tag kind+name/mailbox address, cascade deletes, RouteMailbox, FailNext) for offline tests.
- [X] T011 [P] `services/ticket/internal/sealed/` envelope seal/open + redaction helpers (copy services/asset/internal/sealed, module path updated) + `sealed_test.go` (100%).
- [X] T012 [P] `services/ticket/internal/authz/` Subjects{TenantID,UserID,Roles,ActorKind (agent|inbound|system)} + permission constants (tickets:read, tickets:manage, tickets:delete, tags:manage, rules:manage, mailboxes:manage, stats:read, backup:manage) + Require helpers + IsPlatformAdmin + `SystemFor(tenant)` scoped subject for the inbound edge + `authz_test.go` (100%).
- [X] T013 [P] `services/ticket/internal/blob/` object store (Put streaming+SHA-256, Get, Delete, EnsureBucket) + Fake + `blob_test.go` — copy services/asset/internal/blob with the module path updated.
- [X] T014 [P] `services/ticket/internal/audit/` writer adapter with the action vocabulary (ticket.create/update/delete/assign/status/tags, comment.create/delete, reply.sent/failed, ack.sent/skipped, inbound.created/threaded/duplicate/dropped/refused, rule/tag/mailbox CRUD, backup) + redaction of body, description, requester_email, requester_name, author_email, token, password fields + `audit_test.go`.
- [X] T015 [P] `services/ticket/internal/secrets/` — Source interface {RelayToken, SMTPPassword} backed by warden references (adapt services/ipam/internal/warden) with rotation re-read + Fake + test (values never logged).
- [X] T016 [P] `services/ticket/internal/agents/` — Directory interface {Assignable(tenant) (users holding tickets:manage), Get(tenant,userID)} over pkg/authclient (adapt services/asset/internal/userdir) + Fake + test.
- [X] T017 [P] `services/ticket/internal/events/` publisher (ticket.created/assigned/status_changed/commented/requester_replied → platform:events:<tenant>, ids+metadata only) + `services/ticket/internal/stream/` SSE relay (copy services/asset) + test asserting payloads never contain body/requester fields.
- [X] T018 [P] `services/ticket/internal/metrics/metrics.go` Prometheus collectors (inbound_total{outcome}, rules_errors_total, replies_total{result}, acks_total{result}, status_transitions_total{from,to}) registered on the admin listener.
- [X] T019 `services/ticket/api/openapi/ticket.yaml` — every route from contracts §A with x-freya-permission (health x-freya-public), CSRF on mutations, schemas; embed.go; contract test `services/ticket/tests/contract/openapi_test.go` (parses; every mounted route declared; no route returns body_html).
- [X] T020 [P] `services/ticket/pkg/ticketmanifest/manifest.go` — routes/permissions, roles (ticket admin: all; ticket agent: tickets:read/manage + tags:manage; ticket viewer: tickets:read) via SeedPermissions, UI abilities, nav (Tickets, Dashboard, Rules, Tags, Mailboxes) + `manifest_test.go`.
- [X] T021 App build/wire/run `services/ticket/internal/app/app.go` — freya.New + mesh enroll, store/KEK, secrets, blob (EnsureBucket), mailer, agents, events/stream, metrics, gateway registration via ticketmanifest, mesh HTTP + gRPC surfaces, the inbound edge listener (separate server), admin health/readiness; refuses to start insecure.
- [X] T022 `services/ticket/cmd/ticketsvc/main.go` + bootstrap subcommand (config, migrate, run) with ui.Remote() wiring (the GUI-404 lesson from paperless).
- [X] T023 `services/ticket/internal/repo/repodb/` implementing repo.Store over TimescaleDB + integration test `repodb_integration_test.go` (`//go:build integration`, testcontainers): schema, RLS isolation between two tenants, partial uniques, cascade deletes, RouteMailbox returns routing fields only.

## Phase 3: User Story 1 — Work tickets through their lifecycle (Priority: P1) 🎯 MVP

**Goal**: a tenant-isolated manual ticket queue with assignment, status, priority, history.
**Independent test**: create a ticket manually, list/filter it, assign it, change status/priority, read history, delete it — no email involved (quickstart Scenario 1).

### Tests (write first, must fail)
- [X] T024 [P] [US1] Contract test `services/ticket/tests/contract/tickets_test.go` — create/get/list/update/delete/assign/status/history shapes; filters (status, priority, assignee incl. none, tag, query) and paging total; unspecified status → invalid_status; non-agent assignee → invalid_assignee; tenant B cannot read tenant A's ticket; viewer gets 403 on every mutation.
- [X] T025 [P] [US1] Unit test `services/ticket/internal/tickets/tickets_test.go` — defaults (open/normal/manual), partial update, assign/unassign writes history + event + denormalised name (fake agents), status change sets/clears resolved_at + history + event, delete removes attachments' objects via fake blob.

### Implementation
- [X] T026 [US1] `services/ticket/internal/history/history.go` — Append(field, old, new, actorKind, actorID) and List(ticket) helpers.
- [X] T027 [US1] `services/ticket/internal/tickets/tickets.go` — Service: Create, Get, List, Update, Delete (rows + blob objects), Assign (validate via agents.Directory), SetStatus, with history, events, audit and metrics.
- [X] T028 [US1] HTTP handlers `services/ticket/internal/httpapi/tickets.go` (tickets CRUD, assign, status, history, assignable-users) + registration in `services/ticket/internal/httpapi/server.go` with per-route permission enforcement.
- [X] T029 [P] [US1] gRPC `services/ticket/internal/grpcapi/tickets.go` — Tickets.Create/Get/List for module callers (SPIFFE caller checked against policy) + test.
- [X] T030 [P] [US1] UI schemas + store: `services/ticket/ui/src/schemas/ticket.ts` (ticketSchema, ticketFilterSchema, STATUSES, PRIORITIES), `services/ticket/ui/src/api/types.ts`, `services/ticket/ui/src/stores/tickets.ts` (list/get/create/update/remove/assign/status/history/assignableUsers).
- [X] T031 [US1] UI views: `services/ticket/ui/src/views/tickets/index.vue` (filter bar: query, status, priority, assignee incl. unassigned, tag; paged UiDataTable; row click opens drawer; "New ticket" UiRecordDrawer close-on-save) and `services/ticket/ui/src/views/tickets/drawer.vue` (header chips, status/priority/assignee selects saving immediately, details, history tab, delete with confirm) + unit tests `services/ticket/ui/tests/unit/tickets.spec.ts`.

## Phase 4: User Story 2 — Turn inbound support email into tickets (Priority: P1)

**Goal**: authenticated inbound edge that parses mail, routes by mailbox, dedups, threads replies and stores attachments.
**Independent test**: post fixtures to the inbound edge for a configured mailbox; new ticket, duplicate no-op, threaded reply re-opening a resolved ticket, generic refusals (quickstart Scenario 2).

### Tests (write first, must fail)
- [X] T032 [P] [US2] Unit tests `services/ticket/internal/mailparse/mailparse_test.go` over the fixture corpus — subject/From/To decoding (RFC 2047, legacy charsets), text vs HTML selection and HTML→text fallback, attachments with content-id/inline, In-Reply-To/References lists, Auto-Submitted/Precedence/X-Spam-Score, "(no subject)", safe base file names; limits (part count, depth, attachment size skip, body cap).
- [X] T033 [P] [US2] Fuzz test `services/ticket/internal/mailparse/mailparse_fuzz_test.go` — arbitrary bytes never panic, never exceed limits, never allocate beyond caps.
- [X] T034 [P] [US2] Unit + fuzz tests `services/ticket/internal/thread/thread_test.go` — Token/ParseToken (`[#<id>]`), ReplySubject single "Re:", AppendReference idempotent, Resolve order (comment message id → ticket external id → body token → subject token), tenant confinement.
- [X] T035 [P] [US2] Handler tests `services/ticket/internal/inbound/inbound_test.go` — missing/invalid token (constant-time), query-string token ignored, unknown/inactive mailbox, oversized body → identical generic refusal and nothing stored; created → ticket with attachments in fake blob; duplicate delivery → `duplicate`; reply → requester comment + re-open; reply for another tenant's ticket id → new ticket in the routed tenant; deleted-ticket reply → new ticket; storage down → 503; audit + metrics per outcome.
- [X] T036 [P] [US2] Unit test `services/ticket/internal/mailboxes/mailboxes_test.go` — CRUD, address normalisation, global uniqueness conflict, delete guarded unless force.

### Implementation
- [X] T037 [US2] `services/ticket/internal/mailparse/mailparse.go` — port go-tangra-ticket internal/webhook/mailparse.go onto stdlib + x/text with the caps from config (depth, parts, attachment size) and safe file names.
- [X] T038 [US2] `services/ticket/internal/thread/thread.go` — port internal/thread/thread.go plus Resolve(ctx, store, tenant, parsed) implementing research D4.
- [X] T039 [US2] `services/ticket/internal/mailboxes/mailboxes.go` — Service CRUD + validation + audit; HTTP handlers `services/ticket/internal/httpapi/mailboxes.go`.
- [X] T040 [US2] `services/ticket/internal/inbound/inbound.go` — edge handler: token check (secrets.Source, constant time; Bearer/X-Ticket-Token only), body limit, recipient/message-id extraction (X-Iris-*/X-Ticket-*/headers), RouteMailbox, scoped system subject, dedup, thread → comment + re-open + attachments, else create ticket (source email) + attachments; returns outcome JSON; `services/ticket/internal/inbound/server.go` listener with /healthz.
- [X] T041 [US2] UI: `services/ticket/ui/src/views/mailboxes/index.vue` (list + UiRecordDrawer create/edit, active/auto-ack/template) + `services/ticket/ui/src/schemas/mailbox.ts` + store + unit test.

## Phase 5: User Story 3 — Public replies and internal notes (Priority: P1)

**Goal**: conversation timeline with internal notes and emailed, threaded public replies; safe HTML display.
**Independent test**: add a note (not emailed), send a reply (Mailpit/fake shows threading headers + reference), reply threads back; hostile HTML renders inert (quickstart Scenario 3 + security checks).

### Tests (write first, must fail)
- [X] T042 [P] [US3] Unit tests `services/ticket/internal/mailer/mailer_test.go` — message assembly (From = mailbox, single Re:, reference line once, Message-ID format, In-Reply-To/References), header injection refused (CR/LF in To/Subject/name), TLS modes, plaintext refused unless allow_plaintext, password never in errors/logs; fake SMTP server.
- [X] T043 [P] [US3] Unit tests `services/ticket/internal/comments/comments_test.go` — internal note never sent; reply requires requester + relay (reply_unavailable), records message id + delivery sent/failed, re-opens resolved/closed, updates last_message_id and comment_count, events commented; delete.
- [X] T044 [P] [US3] Unit + fuzz tests `services/ticket/internal/sanitize/sanitize_test.go` — hostile-html.eml yields no script/on*/form/iframe/style element/external src; cid: rewritten to the ticket's attachment URL, unknown cid and remote images dropped; links get rel/target; fuzz never panics.
- [X] T045 [P] [US3] Contract test `services/ticket/tests/contract/comments_test.go` — comments list order/shape, reply/note routes, `/tickets/{id}/body` never returns raw HTML, attachment download of another tenant's id → 404, Content-Disposition + nosniff headers.

### Implementation
- [X] T046 [US3] `services/ticket/internal/mailer/mailer.go` — Sender over net/smtp adapted from services/notification/internal/channel/email (TLS modes, PLAIN only over TLS, HeaderSafe) with threading/auto-reply headers + Fake.
- [X] T047 [US3] `services/ticket/internal/comments/comments.go` — Service: List, AddNote, Reply (thread headers via thread package, mailer, delivery state, re-open, audit, metrics, events), Delete.
- [X] T048 [US3] `services/ticket/internal/sanitize/sanitize.go` — bluemonday policy per research D7 + cid rewriting + text fallback.
- [X] T049 [US3] HTTP handlers `services/ticket/internal/httpapi/comments.go` (comments list/note/reply/delete, `/tickets/{id}/body`, `/tickets/{id}/attachments/{attId}` streaming with safe headers) + gRPC Tickets.AddComment in `services/ticket/internal/grpcapi/tickets.go`.
- [X] T050 [US3] UI: extend `services/ticket/ui/src/views/tickets/drawer.vue` — message view (sanitised HTML in sandboxed srcdoc iframe without allow-scripts/allow-same-origin, plain-text toggle), attachments list with downloads, conversation timeline (internal/sent/incoming/system badges, delivery failed marker, delete), composer (Reply | Internal note, disabled reply without requester) + `services/ticket/ui/src/views/tickets/MessageView.vue` + unit tests.

## Phase 6: User Story 4 — Automate triage with rules (Priority: P2)

**Goal**: ordered CEL rules on new inbound mail with tag/assign/status/priority/drop actions.
**Independent test**: create tag/assign/drop rules; matching fixtures tagged/assigned, spam dropped, broken rule skipped, replies untouched (quickstart Scenario 4).

### Tests (write first, must fail)
- [X] T051 [P] [US4] Unit tests `services/ticket/internal/rules/engine_test.go` — port go-tangra-ticket internal/rules/engine_test.go; every field/operator; ALL/ANY; raw expression override; literal quoting (a value containing quotes/`||` cannot change the expression); invalid condition/expression refused on compile; cost limit and deadline stop pathological expressions/regexes.
- [X] T052 [P] [US4] Fuzz test `services/ticket/internal/rules/compile_fuzz_test.go` — arbitrary condition values/operators never panic and always compile to a boolean expression or a validation error.
- [X] T053 [P] [US4] Unit tests `services/ticket/internal/rules/apply_test.go` — sort order, tags accumulate (EnsureByName creates missing), first match wins for assign/status/priority, any drop discards, erroring rule skipped + counted, history actor "rule".
- [X] T054 [P] [US4] Contract test `services/ticket/tests/contract/rules_test.go` — rules CRUD shapes, validation errors, `/rules/test` dry run, rules:manage required.

### Implementation
- [X] T055 [US4] `services/ticket/internal/rules/engine.go` (CEL env with declared email fields, cost limit, program cache keyed by rule id+version, Evaluate with deadline) and `services/ticket/internal/rules/apply.go` (actions application).
- [X] T056 [US4] `services/ticket/internal/rules/service.go` — rules CRUD with validation + audit; HTTP handlers `services/ticket/internal/httpapi/rules.go` incl. `/rules/test`.
- [X] T057 [US4] Wire rules into `services/ticket/internal/inbound/inbound.go` — evaluate before creating a new ticket only (never on replies), drop → `dropped` outcome + audit + metric, apply actions after creation.
- [X] T058 [US4] UI: `services/ticket/ui/src/views/rules/index.vue` (list with condition/action summaries, enable toggle, sort order) + `services/ticket/ui/src/views/rules/drawer.vue` (builder: name/enabled/sort/match, typed condition rows, action rows tag/assign/status/priority/drop with warning, advanced expression field, "Test" panel) + `services/ticket/ui/src/schemas/rule.ts` + unit tests.

## Phase 7: User Story 5 — Organise with tags (Priority: P2)

**Goal**: tag vocabulary (tag|category) with colours, set on tickets, list filter.
**Independent test**: create tags of both kinds, set on a ticket, filter by tag, rename, delete (quickstart Scenario 5).

### Tests (write first, must fail)
- [X] T059 [P] [US5] Unit test `services/ticket/internal/tags/tags_test.go` — kind default/immutability, per-kind uniqueness (case-insensitive), set ticket tags replaces set + unknown id refused, delete removes links, audit.
- [X] T060 [P] [US5] Contract test `services/ticket/tests/contract/tags_test.go` — CRUD shapes, `?kind=`, set tags route, tags:manage vs tickets:manage split.

### Implementation
- [X] T061 [US5] `services/ticket/internal/tags/tags.go` — Service CRUD + SetTicketTags; HTTP handlers `services/ticket/internal/httpapi/tags.go`.
- [X] T062 [US5] UI: `services/ticket/ui/src/views/tags/index.vue` (kind filter, colour chips, UiRecordDrawer create/edit, delete confirm) + tags editor in `services/ticket/ui/src/views/tickets/drawer.vue` + deterministic auto-colour helper `services/ticket/ui/src/views/tags/colors.ts` + unit tests.

## Phase 8: User Story 6 — Auto-acknowledge, notify, and report (Priority: P3)

**Goal**: loop-safe acknowledgements, events for agents, dashboard, export/import.
**Independent test**: new email gets an ack (auto headers, system comment); auto/bulk/no-reply/self mail gets none; assign emits an event; dashboard figures; backup round-trip (quickstart Scenario 6).

### Tests (write first, must fail)
- [X] T063 [P] [US6] Unit tests `services/ticket/internal/inbound/ack_test.go` — ack sent with template `{{name}}` + reference + Auto-Submitted/Precedence headers and recorded as a system comment with message id; suppressed for auto-submitted, bulk/list/junk precedence, daemon/no-reply senders, self-addressed, auto_ack off, no relay; audit ack.sent/skipped.
- [X] T064 [P] [US6] Unit tests `services/ticket/internal/stats/stats_test.go` — by status/priority/assignee, unassigned open, created/resolved per day over a window (resolved from history).
- [X] T065 [P] [US6] Unit tests `services/ticket/internal/backup/backup_test.go` — FK-ordered export/import, skip/overwrite, non-admin import pinned to caller tenant, full/cross-tenant restore refused for non-admins, no secrets or object bytes exported; fuzz the import parser.

### Implementation
- [X] T066 [US6] Acknowledgement in `services/ticket/internal/inbound/ack.go` (RFC 3834 guards, mailbox template) wired after ticket creation.
- [X] T067 [US6] `services/ticket/internal/stats/stats.go` + HTTP `/stats`; `services/ticket/internal/backup/backup.go` + HTTP `/backup/export|import`; SSE `/stream` in `services/ticket/internal/httpapi/stream.go`.
- [X] T068 [US6] UI: `services/ticket/ui/src/views/dashboard/index.vue` (stat tiles by status/priority, per-assignee bars, unassigned open, created vs resolved per day) + live refresh of the ticket list from the tenant stream in `services/ticket/ui/src/stores/live.ts` + unit tests.

## Phase 9: Platform integration & polish

- [ ] T069 Stack wiring `deploy/stack/`: `ticket` DB + `ticket_app` role in init-db.sql; `ticket` Valkey user; `ticket` compose service (enrolls, mounts policy+kek, RustFS, Mailpit relay, publishes the inbound edge 9957) + `ticket-token` mint init + `configs/ticket.yaml`; relay token + SMTP password seeded in warden and referenced.
  - *Status 2026-09-23*: everything wired and live (DB/role, Valkey user, compose `ticket` + `ticket-token` + `ticket-secrets-init`, `configs/ticket.yaml`, edge :9957 with the dev edge cert, RustFS, Mailpit relay). **Open**: the relay token is a dev `file:` reference (generated into the `ticket-secrets` volume) instead of a warden reference, and Mailpit needs no SMTP password. Warden's `Secrets/GetPassword` authorises a *user* platform token only; the platform has no long-lived service-principal token to put in `secrets.token_file`, so a warden reference cannot be resolved by the module unattended. Needs a platform decision (service principal / token minting for modules) before this item can close.
- [X] T070 Gateway allow-list: add `spiffe://example.org/svc/ticket=/api/ticket;ticket` to gateway-bootstrap; confirm registration (registered:true) and the Tickets menu renders.
  - *Verified*: allow-list row added (gateway-bootstrap), ticket logs `registered:true`, `/api/ticket/v1/health` via the gateway → 200, the gateway serves `/m/ticket/mf-manifest.json`. Menu rendering in a signed-in browser not checked (no operator credentials in this session).
- [X] T071 [P] `services/ticket/deploy/policy.yaml` (gateway forwards; module callers of ticket.v1; ticket calls auth/warden) + `deploy/kek.dev`.
- [X] T072 [P] `services/ticket/deploy/README.md` (inbound edge + relay setup incl. iris/KumoMTA headers, SMTP relay, mailboxes, rules, security) and list ticket in `deploy/stack/README.md`; CHANGELOG entry.
- [X] T073 [P] `services/ticket/pkg/ticketclient/` typed Go client for module-to-module use + test.
- [X] T074 [P] UI end-to-end `services/ticket/ui/tests/e2e/ticket-flow.spec.ts` + `a11y.spec.ts` (axe) — list → drawer → assign/status → note/reply; rules builder; tags; mailboxes.
  - *Written, lint + typecheck clean, `playwright test --list` OK; NOT executed* (needs `E2E_OPERATOR_PASSWORD` for a platform operator; skipped without it).
- [X] T075 Integration test `services/ticket/tests/integration/mail_flow_test.go` (`//go:build integration`; TimescaleDB, Valkey, RustFS, Mailpit): fixture in → ticket → reply out (Mailpit API asserts headers) → reply fixture threads back.
- [X] T076 Coverage + supply chain: `go -C services/ticket test ./...` ≥ 80 % overall, 100 % on sealed/authz/thread and rules compile; `govulncheck` clean; scripts/coverage-gate.sh wired into `make cover`.
  - *Result*: 93 % overall; authz/sealed/secrets/thread 100 %, rules compile path 100 % (gate enforces all); `make vuln` clean; `make lint` clean.
- [ ] T077 Stack smoke: bring up the stack; ticket registers; create a mailbox; `curl` a fixture to the inbound edge → ticket visible in the UI; reply appears in Mailpit; quickstart security checks pass.
  - *Status 2026-09-23 (partial, left open)*: live — registered, health via gateway, mailbox created at module level (DB row; no operator sign-in available), fixtures → `created`/`duplicate`/`threaded`, attachments in RustFS, auto-ack in Mailpit with `Auto-Submitted: auto-replied` + `Precedence: auto_reply` + threading headers, none for auto-submitted mail, requester answer threads back, cross-tenant reference does not thread, header-injection fixture produces no injected header, no PII/token in logs/events/audit. **Not verified live**: ticket visible in the UI and an agent reply via the browser/API (needs a signed-in operator; the agent-reply → Mailpit path is covered by T075 against a real Mailpit).

## Dependencies & sequencing

- Setup (Phase 1) and Foundational (Phase 2) block everything.
- US1 (P1) is the MVP: the manual queue works alone.
- US2 (P1) depends on Foundational only (creates tickets itself) but its UI view of
  bodies/attachments arrives with US3; US3 (P1) depends on US1 (tickets) and uses
  US2's thread package for outbound headers (T038 before T047).
- US4 (P2) depends on US2 (inbound pipeline) and uses tag EnsureByName from the repo
  (Phase 2); US5 (P2) depends on US1 only (tag filter already in the list API).
- US6 (P3) depends on US2 (ack), US1 (stats/events) and all entities (backup).
- Phase 9 after the stories it exercises (T075 needs US2+US3; T077 needs all).

## Parallel execution examples

- Phase 2: T010, T011, T012, T013, T014, T015, T016, T017, T018, T020 in parallel after T009.
- US1: T024 ∥ T025 (tests), then T026 → T027 → T028; T029 and T030 in parallel with T028.
- US2: T032 ∥ T033 ∥ T034 ∥ T035 ∥ T036, then T037 ∥ T038 ∥ T039, then T040, T041.
- US3: T042 ∥ T043 ∥ T044 ∥ T045, then T046 ∥ T048, then T047 → T049 → T050.
- US4: T051 ∥ T052 ∥ T053 ∥ T054, then T055 → T056 → T057, T058 in parallel with T057.
- US5 can run in parallel with US4 (different packages): T059 ∥ T060 → T061 → T062.
- US6: T063 ∥ T064 ∥ T065, then T066 ∥ T067, then T068.

## Implementation strategy

MVP first: Setup + Foundational + US1 (manual ticket queue) → demoable. Then US2
(inbound email) + US3 (replies/notes, safe message view) complete the email loop —
the reference system's core value. Then US4 (rules) and US5 (tags), then US6
(ack/events/dashboard/backup), then Phase 9 (stack, coverage, e2e, smoke).

## Summary

- **Total tasks**: 77 across 9 phases.
- **Per story**: US1 = 8 (T024–T031), US2 = 10 (T032–T041), US3 = 9 (T042–T050), US4 = 8 (T051–T058), US5 = 4 (T059–T062), US6 = 6 (T063–T068).
- **Setup/Foundational/Polish**: 6 + 17 + 9.
- **MVP scope**: Setup + Foundational + US1.
- **Novel vs prior modules**: off-mesh inbound mail edge with mailbox routing, RFC 822/MIME parser, email threading, CEL rules engine, HTML sanitisation, threaded SMTP replies with loop-safe acknowledgements.
