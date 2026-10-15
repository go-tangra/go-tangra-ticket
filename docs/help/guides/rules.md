---
title: Triage rules
summary: Tag, assign, prioritise or drop new email tickets automatically with conditions or a CEL expression, and test a rule before saving it.
kind: guide
order: 40
tags: [ticket, rules, triage, cel, automation]
requires: rules:manage
---
Rules run on every **new email ticket**, right after it is created and
before the acknowledgement. They do not run on manual tickets or on replies
to existing tickets. Manage them on **Rules** (needs `rules:manage`).

## Parts of a rule

- **Name**, **Enabled**, and **Sort order**: lower runs first.
- **When**: conditions combined with **Match** `all` or `any`, or an
  **Expression (CEL)**, which overrides the conditions.
- **Then**: one to 20 actions.

### Conditions

| Field | Kind | Operators |
|---|---|---|
| `subject`, `body`, `from`, `fromName`, `recipient`, `fromDomain` | text | `contains`, `not_contains`, `equals`, `not_equals`, `starts_with`, `ends_with`, `matches` |
| `hasAttachments` | true/false | `equals`, `not_equals` |
| `spamScore` | number | `gt`, `gte`, `lt`, `lte`, `eq`, `neq` |

- Text comparisons ignore case, except `matches`, which takes an RE2 regular
  expression against the raw value (start it with `(?i)` to ignore case).
- `from` is the sender address, `fromDomain` its domain in lower case,
  `recipient` the mailbox address the message was sent to.
- `body` is the plain text; rules see its first 64 KiB.
- `hasAttachments` is true even for attachments too large to store.
- `spamScore` is read from the `X-Spam-Score` header; 0 without one.

### Expressions

An expression is a CEL boolean over the same fields, for example:

```text
fromDomain == "bigcustomer.com" && subject.lowerAscii().contains("outage")
```

It is compiled and type-checked when you save; a mistake is refused with
`invalid_rule` and the reason.

### Actions

| Action | Effect |
|---|---|
| tag | adds tags (**Kind** `tag` or `category`); names that do not exist yet are created |
| assign | assigns the ticket; the user must hold `tickets:manage` (checked when you save) |
| status | sets the status |
| priority | sets the priority |
| drop | discards the message: no ticket and no acknowledgement |

## When several rules match

- Tags from every matching rule are added.
- For assign, status and priority, the first matching rule (by sort order)
  that sets one wins.
- If any matching rule drops the message, it is dropped.

Changes made by rules appear on the ticket's **History** with actor `rule`.

## Test a rule

The **Test** panel of the rule editor evaluates the rule you are editing
against a sample message (**Subject**, **Sender email**, **Sender name**,
**Recipient**, **Spam score**, **Has attachments**, **Body**). **Run test**
shows whether it matches and which actions it would take. Nothing is saved.

## Safety limits

Each rule runs under a cost limit and all rules of one message under a short
deadline (by default 50 ms), set by the operator. A rule that fails while
running is skipped and counted; the message is still turned into a ticket.
A tenant can have at most 500 rules by default.

Related: [example: route billing mail](../examples/billing-rule.md),
[inbound email](email.md).
