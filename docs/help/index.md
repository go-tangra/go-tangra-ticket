---
title: Tickets
summary: An email helpdesk - tickets with a conversation of internal notes and emailed replies, inbound mail that opens or threads into tickets, triage rules, tags and mailboxes.
kind: guide
order: 0
tags: [ticket, helpdesk, overview, email]
requires: tickets:read
---
Tickets is the platform's helpdesk. A ticket is one request: a subject, a
status, a priority, an assignee and tags, with a conversation underneath.
Requests arrive by email to a support mailbox, or agents open them by hand.
Agents answer by email from the ticket, and the requester's answers land back
in the same ticket.

## Concepts

- **Ticket** — status `open`, `in_progress`, `pending`, `resolved` or
  `closed`; priority `low`, `normal`, `high` or `urgent`; an optional
  assignee; tags and categories. Source `email` or `manual`.
- **Conversation** — the timeline of a ticket: the requester's emails,
  **replies** that agents send to the requester by email, and **internal
  notes** that only agents see.
- **Mailbox** — a support address such as `support@example.org`. Mail sent to
  an active mailbox becomes a ticket of your tenant; replies are sent from it.
- **Rule** — a triage rule that runs on every new email ticket: when the
  message matches, it tags, assigns, sets status or priority, or drops it.
- **Tag** and **category** — labels for tickets. Categories are tags of the
  kind `category`.

Every change is shown live to everyone viewing the ticket list.

## Pages in the portal

| Page | Shows | Needs |
|---|---|---|
| Tickets | the ticket list with search and filters; open a ticket for its conversation, details and history | `tickets:read` |
| Dashboard | totals by status, priority and assignee; tickets created and resolved per day | `stats:read` |
| Rules | triage rules, in evaluation order | `rules:manage` |
| Tags | tags and categories | `tickets:read` (changes: `tags:manage`) |
| Mailboxes | support addresses and their acknowledgements | `mailboxes:manage` |

Your role decides what you can do; see
[permissions and roles](reference.md#permissions-and-roles).

## Where to go next

- [Get started](getting-started.md): your first ticket and reply.
- [Work tickets](guides/work-tickets.md): status, assignment, notes, replies.
- [Mailboxes and inbound email](guides/email.md)
- [Triage rules](guides/rules.md)
- [Example: route billing mail](examples/billing-rule.md)
- [Reference](reference.md): fields, states, permissions, API and limits.
