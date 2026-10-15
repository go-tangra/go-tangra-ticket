---
title: Reference
summary: Ticket, comment, tag, mailbox and rule fields; states; permissions and roles; API routes, errors and limits.
kind: reference
order: 60
tags: [ticket, reference, permissions, api, limits]
requires: tickets:read
---
## Ticket fields

| Field | Meaning |
|---|---|
| `subject` | 1–998 characters |
| `description` | up to 1 MiB; for email tickets the message text |
| `status` | `open`, `in_progress`, `pending`, `resolved`, `closed` |
| `priority` | `low`, `normal` (default), `high`, `urgent` |
| `source` | `manual` or `email` |
| `requester_email`, `requester_name` | up to 320 and 200 characters |
| `recipient`, `mailbox_id` | the mailbox an email ticket arrived at |
| `assignee_id`, `assignee_name` | a user holding `tickets:manage` |
| `tags` | tags and categories |
| `attachments` | files of the original message |
| `has_html` | the original message has an HTML part |
| `comment_count` | entries in the conversation |
| `created_at`, `updated_at`, `resolved_at` | `resolved_at` is set on entering `resolved` and cleared by any other status |

## Comment fields

| Field | Meaning |
|---|---|
| `body` | 1 byte to 1 MiB |
| `internal` | true for internal notes (never emailed) |
| `author_kind` | `agent`, `requester` or `system` |
| `delivery` | `none`, `sent` or `failed` (replies and acknowledgements) |
| `message_id` | the email Message-ID, used for threading |

## History entries

`field` (`status`, `priority`, `assignee`), `old_value`, `new_value` and
`actor_kind`: `agent`, `rule`, `inbound` (a requester's mail re-opened the
ticket) or `system`.

## Tags, mailboxes, rules

| Record | Fields |
|---|---|
| tag | `name` (1–100 characters, unique in the tenant), `kind` (`tag` or `category`, fixed after creation), `color`, `description` |
| mailbox | `address` (unique across all tenants), `display_name`, `active`, `auto_ack`, `auto_ack_template` (up to 8192 characters, `{{name}}` placeholder) |
| rule | `name` (1–200), `enabled`, `sort_order`, `match` (`all` or `any`), up to 50 `conditions`, `expression` (CEL, up to 4096 characters), 1–20 `actions` |
| rule condition | `field`, `operator`, `value` (up to 1000 characters) |
| rule action | `type` `tag` (`tag_kind`, up to 20 `tag_names`), `assign` (`assignee_id`), `status`, `priority` or `drop` |

## Permissions and roles

| Permission | Allows |
|---|---|
| `tickets:read` | list and read tickets, conversations, attachments, history, tags and assignable users; the live event stream |
| `tickets:manage` | create and edit tickets, assign, change status, set tags, add notes, send replies, delete comments |
| `tickets:delete` | delete tickets |
| `tags:manage` | the tag and category vocabulary |
| `rules:manage` | triage rules |
| `mailboxes:manage` | mailboxes and acknowledgements |
| `stats:read` | the dashboard |
| `backup:manage` | export and import the tenant's ticket data |

| Module role | Permissions |
|---|---|
| Tickets administrator | all eight |
| Tickets agent | `tickets:read`, `tickets:manage`, `tags:manage` |
| Tickets viewer | `tickets:read` |

Built-in roles: `owner` and `admin` hold the administrator set, `operator`
the agent set, `member` and `auditor` the viewer set.

## API

The browser API is under `/api/ticket/v1`, through the gateway. Changing
requests need the `X-CSRF-Token` header. Request bodies with unknown fields
are refused.

| Route | Permission |
|---|---|
| `GET /tickets`, `GET /tickets/{id}`, `/tickets/{id}/body`, `/tickets/{id}/comments`, `/tickets/{id}/history`, `/tickets/{id}/attachments/{att_id}` | `tickets:read` |
| `POST /tickets`, `PUT /tickets/{id}`, `POST /tickets/{id}/status`, `/assign`, `/tags`, `/comments`, `/reply`; `DELETE /comments/{id}` | `tickets:manage` |
| `DELETE /tickets/{id}` | `tickets:delete` |
| `GET /tags`, `GET /assignable-users`, `GET /stream` | `tickets:read` |
| `POST /tags`, `PUT /tags/{id}`, `DELETE /tags/{id}` | `tags:manage` |
| `GET /rules`, `GET /rules/{id}`, `POST /rules`, `PUT /rules/{id}`, `DELETE /rules/{id}`, `POST /rules/test` | `rules:manage` |
| `GET /mailboxes`, `POST /mailboxes`, `PUT /mailboxes/{id}`, `DELETE /mailboxes/{id}` | `mailboxes:manage` |
| `GET /stats` | `stats:read` |
| `POST /backup/export`, `POST /backup/import` | `backup:manage` |

`GET /tickets` filters by `status`, `priority`, `assignee_id` (`none` for
unassigned), `tag_id` and `query`, and takes `page`, `page_size`, `sort` and
`order`. `POST /tickets/{id}/comments` adds an internal note:

```json
{"body": "Checked: paper tray 2 is worn.", "internal": true}
```

`GET /tickets/{id}/body` returns the original message as sanitised HTML and
plain text; the raw HTML is never returned, except in a backup export.

### Backup

An export carries the tenant's tags, mailboxes, rules, tickets,
conversations, history and attachment metadata; never secrets and never the
attachment files themselves, which stay in object storage. An import (`mode` `skip` or `overwrite`) loads it into your
tenant. Importing into another tenant, or a full restore that first wipes the
tenant, needs the `platform-admin` role.

### Errors

| Status | `reason` | Typical cause |
|---|---|---|
| 400 | `malformed_body` | invalid JSON or an unknown field |
| 403 | `forbidden` | the permission is missing |
| 404 | `ticket_not_found`, `comment_not_found`, `not_found` | no such item in your tenant |
| 409 | `conflict` | a tag name or mailbox address already exists; deleting a mailbox that tickets still refer to |
| 409 | `reply_unavailable` | no requester address, no outbound mail, or no mailbox to send from |
| 422 | `invalid_status`, `invalid_assignee` | unknown status; the user cannot be assigned |
| 422 | `invalid_rule` | the rule does not compile or a field is invalid (`detail` names it) |
| 422 | `validation_failed` | another field is invalid |
| 502 | `delivery_failed` | the reply was saved but the mail relay refused it |
| 503 | `directory_unavailable` | the user directory (auth) did not answer |

## Limits

| Limit | Value |
|---|---|
| subject | 998 characters |
| description, comment body | 1 MiB |
| rules per tenant | `rules.max_rules` (default 500) |
| rule conditions, actions, tag names per action | 50, 20, 20 |
| rule body text evaluated | first 64 KiB |
| inbound message | `inbound.max_body_bytes` (default 10 MiB) |
| one attachment | `inbound.max_attachment_bytes` (default 25 MiB) |
| backup import body | 64 MiB |
