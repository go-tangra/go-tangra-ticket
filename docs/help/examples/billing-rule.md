---
title: "Example: route billing mail"
summary: Drop obvious spam, then send everything about invoices to the billing team with a category, a higher priority and an assignee.
kind: example
order: 50
tags: [ticket, example, rules, triage]
requires: rules:manage
---
**Goal.** Customers write to `support@example.org` and, more and more, to
`billing@example.org`. Messages to the billing address, and any message that
talks about an invoice, should land with Maria from the billing team, in the
category *Billing*, at priority `high`. Messages your mail filter scored as
spam (`X-Spam-Score` 5 or more) should not become tickets at all.

**Plan.** Two mailboxes and two rules. The spam rule runs first.

## 1. Mailboxes

On **Mailboxes**, create `support@example.org` and `billing@example.org`,
both **Active**. Turn on **Send acknowledgements** where you want them.

## 2. Drop spam

On **Rules**, choose **New rule**:

| Field | Value |
|---|---|
| Name | `Drop spam` |
| Sort order | `0` |
| When | Match `all`: `spamScore` `gte` `5` |
| Then | `drop` |

## 3. Billing

Another rule:

| Field | Value |
|---|---|
| Name | `Billing` |
| Sort order | `10` |
| When | Match `any`: `recipient` `equals` `billing@example.org`; `subject` `contains` `invoice` |
| Then | `tag`, kind `category`, names `Billing`; `priority` `high`; `assign` Maria |

The *Billing* category is created the first time the rule matches, if it
does not exist yet. Saving the rule checks that Maria holds
`tickets:manage`.

The same through the API, `POST /api/ticket/v1/rules`:

```json
{
  "name": "Billing",
  "enabled": true,
  "sort_order": 10,
  "match": "any",
  "conditions": [
    {"field": "recipient", "operator": "equals", "value": "billing@example.org"},
    {"field": "subject", "operator": "contains", "value": "invoice"}
  ],
  "actions": [
    {"type": "tag", "tag_kind": "category", "tag_names": ["Billing"]},
    {"type": "priority", "priority": "high"},
    {"type": "assign", "assignee_id": "<Maria's user id>"}
  ]
}
```

`GET /api/ticket/v1/assignable-users` lists the user ids you can assign.

## 4. Test before you rely on it

In the **Test** panel of the *Billing* rule, enter the subject `Invoice
2026-10 is missing`, sender `ana@customer.example` and recipient
`support@example.org`, and choose **Run test**. It matches (the subject
contains `invoice`, in any case) and lists the three actions.

Through the API, `POST /api/ticket/v1/rules/test`:

```json
{
  "rule": {
    "name": "Billing",
    "match": "any",
    "conditions": [{"field": "subject", "operator": "contains", "value": "invoice"}],
    "actions": [{"type": "priority", "priority": "high"}]
  },
  "sample": {"subject": "Invoice 2026-10 is missing", "from": "ana@customer.example", "recipient": "support@example.org"}
}
```

answers `{"matched": true, "actions": [...]}`.

## What happens

| Message | Result |
|---|---|
| to `billing@example.org`, subject "Question" | ticket, category *Billing*, `high`, assigned to Maria |
| to `support@example.org`, subject "Invoice wrong" | the same |
| to `support@example.org`, subject "Printer broken" | ticket, `normal`, unassigned |
| any address, `X-Spam-Score: 7.2` | dropped: no ticket, no acknowledgement |

The ticket's **History** shows the priority and assignee changes with actor
`rule`.

Related: [triage rules](../guides/rules.md), [mailboxes](../guides/email.md).
