import type { TicketPriority, TicketStatus } from '../../api/services';

// "TICKET_STATUS_IN_PROGRESS" -> "In Progress"
export function humanizeEnum(v?: string): string {
  if (!v) return '';
  return v
    .split('_')
    .slice(2)
    .map((w) => w.charAt(0) + w.slice(1).toLowerCase())
    .join(' ');
}

export function statusColor(s: TicketStatus): string {
  switch (s) {
    case 'TICKET_STATUS_OPEN':
      return 'blue';
    case 'TICKET_STATUS_IN_PROGRESS':
      return 'processing';
    case 'TICKET_STATUS_PENDING':
      return 'gold';
    case 'TICKET_STATUS_RESOLVED':
      return 'green';
    case 'TICKET_STATUS_CLOSED':
      return 'default';
    default:
      return 'default';
  }
}

export function priorityColor(p: TicketPriority): string {
  switch (p) {
    case 'TICKET_PRIORITY_URGENT':
      return 'red';
    case 'TICKET_PRIORITY_HIGH':
      return 'orange';
    case 'TICKET_PRIORITY_NORMAL':
      return 'blue';
    case 'TICKET_PRIORITY_LOW':
      return 'default';
    default:
      return 'default';
  }
}

export const statusOptions: { value: TicketStatus; label: string }[] = [
  { value: 'TICKET_STATUS_OPEN', label: 'Open' },
  { value: 'TICKET_STATUS_IN_PROGRESS', label: 'In Progress' },
  { value: 'TICKET_STATUS_PENDING', label: 'Pending' },
  { value: 'TICKET_STATUS_RESOLVED', label: 'Resolved' },
  { value: 'TICKET_STATUS_CLOSED', label: 'Closed' },
];

export const priorityOptions: { value: TicketPriority; label: string }[] = [
  { value: 'TICKET_PRIORITY_LOW', label: 'Low' },
  { value: 'TICKET_PRIORITY_NORMAL', label: 'Normal' },
  { value: 'TICKET_PRIORITY_HIGH', label: 'High' },
  { value: 'TICKET_PRIORITY_URGENT', label: 'Urgent' },
];
