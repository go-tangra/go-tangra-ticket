---
title: Work tickets
summary: Find tickets, change status and priority, assign, tag, read the original message, write notes and replies, follow the history and delete.
kind: guide
order: 20
tags: [ticket, status, assign, replies, notes, history]
requires: tickets:read
---
## Find tickets

**Tickets** lists the tenant's tickets, newest first, and updates live.
Search the subject and requester with **Search (subject / requester)**, and
filter by **Status**, **Priority**, **Assignee** (including unassigned) and
**Tag**. Sorting by status or priority follows the workflow and urgency
order, with unassigned tickets last when sorting by assignee.

Open a ticket to see three tabs: **Conversation**, **Details** and
**History**.

## Status, priority and assignee

Changing these needs `tickets:manage`.

| Status | Typical use |
|---|---|
| `open` | new, or the requester wrote again |
| `in_progress` | someone works on it |
| `pending` | waiting for the requester or a third party |
| `resolved` | answered; the time is recorded as **Resolved** |
| `closed` | done |

Any status can follow any other. Moving away from `resolved` clears the
resolved time, closing included. A `resolved` or `closed` ticket goes back to
`open` when the requester writes again or an agent sends a reply.

**Priority** is `low`, `normal` (the default), `high` or `urgent`.

**Assignee** offers the users who hold `tickets:manage`; choosing anybody
else is refused (`invalid_assignee`). Clear it to unassign.

Every change of status, priority and assignee is listed on **History** with
who made it: an agent, a rule, inbound mail (re-opening) or the system.

## Tags

The tag buttons under the ticket toggle its tags and categories (needs
`tickets:manage`). The vocabulary itself is kept on **Tags** (needs
`tags:manage`): a name, a kind (`tag` or `category`, fixed after creation), a
colour and a description.

## Conversation

The top of the conversation shows the original **Message**: for email
tickets the HTML as received, cleaned of scripts and remote images (images
sent inside the email are shown), or the plain text. Attachments are listed
with their size and download as files.

Below it, each entry is marked:

| Mark | Entry |
|---|---|
| **Incoming** | an email from the requester |
| **Sent** | a reply an agent emailed to the requester |
| **Internal** | an internal note |
| **System** | an automatic acknowledgement |

To write, choose **Reply** (emailed to the requester) or **Internal note**
(agents only), type and choose **Send reply** or **Add note**. Both need
`tickets:manage`.

A reply needs a requester email address, outbound mail set up by the
operator and an active mailbox to send from; otherwise it is refused with
`reply_unavailable`. A reply is saved before it is sent. If sending fails,
it stays marked **Delivery failed** (`delivery_failed`) and is not retried.

**Delete comment** removes an entry and its attachments (needs
`tickets:manage`).

## Edit and delete

**Edit** changes the subject, description and priority. **Delete** removes
the ticket with its whole conversation, history and attachments (needs
`tickets:delete`). This cannot be undone.

Related: [inbound email](email.md), [reference](../reference.md#ticket-fields).
