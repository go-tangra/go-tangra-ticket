---
title: Get started
summary: Open a ticket by hand, add an internal note, email a reply to the requester and resolve the ticket.
kind: guide
order: 10
tags: [ticket, getting-started]
requires: tickets:manage
---
This walk-through works one ticket from start to finish. You need
`tickets:manage`: the *Tickets agent* and *Tickets administrator* roles and
the built-in `operator`, `admin` and `owner` roles have it. Sending replies
also needs at least one active mailbox and outbound mail set up by the
operator; see [mailboxes](guides/email.md).

## 1. Open a ticket

1. Open **Tickets** and choose **New ticket**.
2. Fill in the **Subject**, for example `Printer on floor 2 jams`, and a
   description.
3. Fill in **Requester email** and **Requester name** so you can reply by
   email.
4. Optionally choose a priority and an **Assignee**, then choose **Create**.

The ticket starts `open` with source `manual`.

## 2. Add an internal note

Open the ticket. On the **Conversation** tab, choose **Internal note**, type
`Checked: paper tray 2 is worn.` and choose **Add note**. Notes are never
emailed; only agents see them.

## 3. Reply to the requester

Choose **Reply**, type your answer and choose **Send reply**. The reply is
emailed to the requester:

- from the ticket's mailbox (for a manual ticket, the first active mailbox),
- with the subject `Re: Printer on floor 2 jams`,
- with the line `Ticket reference: [#<ticket id>]` at the end.

When the requester answers that email, the answer is added to this ticket's
conversation.

If sending fails, the reply stays in the conversation marked **Delivery
failed**. It is not retried: send it again once mail works.

## 4. Resolve

Set **Status** to `Resolved`. The ticket records when it was resolved. If the
requester writes again, or an agent sends another reply, the ticket goes back
to `open`.

## Next steps

- [Assign, tag and follow tickets](guides/work-tickets.md)
- [Receive tickets by email](guides/email.md)
- [Triage new mail automatically](guides/rules.md)
