# Implementation Plan: Ticket Service

**Branch**: `014-ticket-service` | **Date**: 2026-09-23 | **Spec**: [spec.md](./spec.md)

**Input**: Feature specification from `/specs/014-ticket-service/spec.md`

## Summary

A tenant-scoped, email-driven helpdesk module replicating go-tangra-ticket:
inbound support mail (raw RFC 822 from the mail relay) becomes tickets with
decoded bodies and attachments; replies thread back onto the same ticket; agents
work tickets through a status/priority/assignee lifecycle with tags, internal
notes and emailed public replies carrying threading headers; ordered CEL rules
tag/assign/set status/set priority/drop inbound mail; new tickets get a
loop-safe acknowledgement. Freya adaptations: per-tenant mailbox routing behind an
off-mesh inbound edge authenticated by a warden-held relay token; SPIFFE mTLS +
gateway platform token; TimescaleDB + RLS; object-store attachments; platform
users as agents; events for notification and live UI. Enhancements over the
source: enforced permissions, tenant-scoped downloads, history, tag filter,
dashboard, delete cleanup, export/import, server-side HTML sanitisation.

## Technical Context

**Language/Version**: Go 1.26 (as every Freya service); UI TypeScript/Vue 3.

**Primary Dependencies**: the Freya framework (`github.com/go-freya/freya`:
SPIFFE mTLS gRPC, OpenAPI-validated HTTP edge, identity, audit, sealed envelopes,
gateway registration, observe metrics); `pgx` + TimescaleDB; Valkey event bus;
`minio/minio-go/v7` object storage (reusing the paperless/asset `blob` package);
`golang.org/x/text` charsets (mail decoding); **new**: `github.com/google/cel-go`
(rules, research D6) and `github.com/microcosm-cc/bluemonday` (HTML sanitisation,
D7); stdlib `net/smtp` mailer (D5); `pkg/authclient` (platform token + user
directory) and the warden client (relay token, SMTP password). UI: the shared
`@freya/ui` kit (FlyonUI/Tailwind/Zod) as a Module-Federation remote.

**Storage**: TimescaleDB with per-tenant RLS on every `ticket_*` table; partial
unique indexes for idempotent ingestion; `ticket_audit_events` hypertable;
attachments in object storage under `tenants/<t>/tickets/<ticket>/<attachment>`
(data-model.md).

**Inbound edge**: a separate listener (`POST /inbound/mail`) authenticated by the
relay token, bounded bodies/parts, recipient → mailbox → tenant routing through a
system-scoped lookup, all writes under a scoped system subject pinned to that
tenant (D2).

**Testing**: Go `testing` with the `testrt` runtime and a `memstore` fake; blob,
mailer, user directory and secret source behind interfaces with fakes, so
ingestion, threading, rules, replies and authorization are unit-tested offline;
`.eml` fixture corpus (charsets, HTML, inline images, attachments, auto-submitted,
spam, hostile HTML); fuzz tests for the MIME parser, thread-token parsing, CEL
condition compilation and the HTML sanitiser; contract tests over OpenAPI +
proto; integration suite (testcontainers: TimescaleDB, Valkey, RustFS, Mailpit)
behind `//go:build integration`. Coverage gate ≥ 80 % overall, 100 % on
sealed/authz/thread/rules-compile. UI: vitest unit tests + Playwright e2e/axe.

**Target Platform**: Linux container in `deploy/stack` behind the gateway, with
RustFS for attachments, Mailpit as the dev relay, and the inbound edge published
for the relay; calls auth + warden over the mesh.

**Project Type**: Web service (Go backend + gRPC + OpenAPI HTTP + off-mesh inbound
edge) with object storage, outbound SMTP, a rules engine and a Module-Federation UI.

**Performance Goals**: an inbound email becomes a visible ticket in < 5 s
(SC-004); list/search/dashboard < 3 s at 100 k tickets per tenant (SC-009); rule
evaluation < 50 ms per message for 100 rules (cost-limited).

**Constraints**: per-tenant RLS; relay token + SMTP password from warden, never
returned or logged; no query-string tokens; bodies/PII excluded from logs and
events; raw HTML never served; outbound headers validated; RFC 3834 loop guards;
bounded MIME parsing; CEL cost limit + deadline; full restore platform-admin only.

**Scale/Scope**: 100 k tickets per tenant; six prioritized user stories (manual
lifecycle, inbound email, replies/notes, rules, tags, acknowledgement/events/
dashboard); ports gRPC 9955, HTTP 9956, admin 9840, inbound 9957.

## Constitution Check

*GATE: Must pass before Phase 0 research. Re-check after Phase 1 design.*

- **I. Secure by Default**: refuses to start without a relay-token reference and
  KEK; SMTP plaintext only with an explicit development flag; raw HTML never
  served; bounded inbound parsing; TLS everywhere. PASS.
- **II. Zero Trust Service Communication**: mesh calls SPIFFE mTLS (auth, warden);
  browser via gateway platform token; the one non-mesh surface (inbound edge) is a
  separate listener with its own credential, constant-time checked, no shared
  secrets in config (warden references). PASS.
- **III. Least Privilege & Tenant Isolation**: per-tenant RLS on every table;
  mailbox routing through a narrow system-scoped lookup; inbound writes under a
  scoped system subject pinned to the routed tenant; attachment downloads RLS-
  checked; permissions per route with seeded least-privilege roles. PASS.
- **IV. Test-First with Security Verification (NON-NEGOTIABLE)**: contract/unit/
  integration tests precede implementation; external systems behind fakes;
  negative + fuzz tests for MIME parsing, threading tokens, rule compilation, HTML
  sanitisation, header injection, cross-tenant threading and downloads. PASS.
- **V. Defense in Depth & Observability**: gateway edge + module authz + RLS;
  sanitisation + sandboxed srcdoc + edge CSP for HTML; CEL cost limits; append-only
  audit of every mutation, ingestion outcome and outbound mail; Prometheus metrics;
  health/readiness on both listeners. PASS.
- **VI. Supply-Chain Integrity**: two new dependencies (cel-go, bluemonday) —
  widely used, actively maintained, required for parity (CEL rules) and SR-003
  (sanitisation); others reused; `go.sum` pinned; `govulncheck` in CI. PASS.
- **VII. Simplicity & Explicitness**: explicit wiring; mail parsing, threading,
  rules, mailer and sanitisation are separate packages behind small interfaces;
  the source's legacy rule fields are not carried over. PASS.

No Constitution violations. Security Requirements SR-001..006 map to research
decisions D1–D11 and to test tasks. Post-design re-check: PASS (the design adds no
new trust boundary beyond those listed in the spec).

## Project Structure

### Documentation (this feature)

```
specs/014-ticket-service/
├── plan.md              # This file
├── research.md          # Phase 0 output (D1–D14, supply chain, STRIDE)
├── data-model.md        # Phase 1 output
├── quickstart.md        # Phase 1 output
├── contracts/           # Phase 1 output (HTTP, inbound edge, gRPC, events, interfaces)
└── tasks.md             # Phase 2 output (/speckit-tasks)
```

### Source Code (repository root)

```
services/ticket/
├── go.mod                       # module github.com/go-freya/freya/services/ticket
├── cmd/ticketsvc/               # run + bootstrap/migrate
├── api/
│   ├── openapi/ticket.yaml      # browser routes (x-freya-permission / x-freya-public)
│   └── proto/ticket/v1/         # gRPC: Tickets (create/get/list/add-comment) for modules
├── internal/
│   ├── app/                     # wiring (freya.New, stores, blob, mailer, warden, inbound edge, gateway reg)
│   ├── config/                  # db/valkey/kek/object_store/smtp/inbound/limits/gateway
│   ├── store/ + repo/ + repodb/ # migrations (RLS, partial uniques, audit hypertable), models, SQL
│   ├── memstore/                # in-memory repo fake
│   ├── sealed/ authz/ audit/    # envelopes, permission checks, audit vocabulary
│   ├── blob/                    # object store (reused) + fake
│   ├── mailparse/               # RFC 822/MIME decoding (ported, fuzzed)
│   ├── thread/                  # reference token, reply subject, threading resolution
│   ├── rules/                   # CEL compile/evaluate (cost-limited) + action application
│   ├── sanitize/                # bluemonday policy + cid rewriting
│   ├── mailer/                  # SMTP sender with threading headers + fake
│   ├── agents/                  # assignable users / names over authclient (+ fake)
│   ├── secrets/                 # warden-backed relay token + SMTP password (+ fake)
│   ├── inbound/                 # inbound edge handler: auth, routing, dedup, thread, rules, create, ack
│   ├── tickets/ comments/ tags/ mailboxes/ history/   # domain services
│   ├── stats/ backup/           # dashboard aggregates, export/import
│   ├── events/ stream/          # platform event publisher + SSE relay
│   ├── metrics/                 # Prometheus collectors
│   └── httpapi/ grpcapi/        # gateway HTTP + mesh gRPC surfaces
├── pkg/
│   ├── ticketmanifest/          # gateway manifest (routes/permissions/abilities/nav) + SeedPermissions/roles
│   └── ticketclient/            # typed module-to-module client
├── testdata/mail/               # .eml fixture corpus
├── ui/                          # @freya/ui MF remote: tickets (list + drawer), rules, tags, mailboxes, dashboard
├── deploy/                      # policy.yaml, kek.dev, README
├── Dockerfile Makefile buf.yaml buf.gen.yaml
deploy/stack/                    # compose service + ticket-token + init-db role/DB + configs/ticket.yaml + gateway allow-list
```

**Structure Decision**: mirrors services/asset and services/paperless (proven
module + object-storage layout) and inventory's off-mesh edge, with the ticket-
specific packages — `mailparse`, `thread`, `rules`, `sanitize`, `mailer`,
`inbound` — each behind small interfaces with fakes for offline tests.

## Complexity Tracking

The inbound edge, outbound SMTP, CEL rules and HTML sanitisation add surface
beyond a CRUD module, but each is required for parity with the source (or by
SR-003/SR-005) and is isolated in its own package with fakes; the two new
dependencies are justified in research (supply-chain note). No Constitution
violations.
