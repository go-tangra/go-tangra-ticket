---
title: Mailboxes and inbound email
summary: Set up support mailboxes and acknowledgements, and learn how inbound email opens new tickets or threads into existing ones.
kind: guide
order: 30
tags: [ticket, email, mailboxes, acknowledgements, threading]
requires: mailboxes:manage
---
## Mailboxes

A mailbox is a support address of your tenant. Manage them on **Mailboxes**
(needs `mailboxes:manage`):

| Field | Meaning |
|---|---|
| **Address** | the support address, for example `support@example.org`. An address belongs to one tenant only. |
| **Display name** | the From name of replies and acknowledgements (default `Support`) |
| **Active (accept inbound mail)** | off: mail to the address is refused; replies on its existing tickets are still sent from it |
| **Send acknowledgements** | answer each new email ticket automatically |
| **Acknowledgement template** | the acknowledgement text; `{{name}}` becomes the requester's name |

A mailbox that tickets refer to cannot be deleted (`conflict`); turn
**Active** off instead.

The platform's mail relay must deliver the address's mail to the ticket
module; that part is set up by the operator.

## From email to ticket

When a message reaches an active mailbox, it either joins an existing ticket
of your tenant or opens a new one.

It **joins** a ticket when:

1. its `In-Reply-To` or `References` header names a message of that ticket
   (an email the ticket received, a reply or an acknowledgement it sent), or
2. its body or subject carries the ticket's reference `[#<ticket id>]`. Every
   reply and acknowledgement ends with `Ticket reference: [#<ticket id>]`, so
   an answer that lost its headers still finds its way.

The message is added to the conversation as **Incoming**. A `resolved` or
`closed` ticket goes back to `open`.

Otherwise it **opens** a ticket: status `open`, priority `normal`, source
`email`, the message's subject, sender (name and address) and text, and the
mailbox as recipient. Then the [triage rules](rules.md) run, and finally the
acknowledgement is sent.

The same message delivered twice (same `Message-Id`) is stored once.

## Attachments

Attachments are stored with the message and can be downloaded from the
ticket. An attachment over the size limit (by default 25 MiB) is not stored;
an internal note from *Inbound mail* names it. The same note appears when a
message has more MIME parts or nesting than allowed.

## Acknowledgements

With **Send acknowledgements** on, a new email ticket gets an automatic
answer from its mailbox: your template (or a built-in greeting when it is
empty) with the reference line appended. It appears in the conversation as
**System**.

To avoid mail loops, no acknowledgement is sent when the message:

- is itself automatic (`Auto-Submitted` other than `no`, or `Precedence`
  `bulk`, `list`, `junk` or `auto_reply`);
- comes from a daemon or no-reply sender;
- comes from one of the support mailboxes.

Acknowledgements are marked `Auto-Submitted: auto-replied` so that other
systems do not answer them. Replies to existing tickets are never
acknowledged.

Related: [work tickets](work-tickets.md), [triage rules](rules.md).
