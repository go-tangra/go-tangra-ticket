// Field / operator vocabulary for the Cloudflare-style rule builder. These
// MUST match the backend CEL engine (internal/rules/engine.go).

export const fieldOptions: { value: string; label: string }[] = [
  { value: 'subject', label: 'Subject' },
  { value: 'body', label: 'Body' },
  { value: 'from', label: 'Sender email' },
  { value: 'fromName', label: 'Sender name' },
  { value: 'fromDomain', label: 'Sender domain' },
  { value: 'recipient', label: 'Recipient' },
  { value: 'hasAttachments', label: 'Has attachments' },
  { value: 'spamScore', label: 'Spam score' },
];

// Boolean fields render a yes/no value instead of operator + text.
export const booleanFields = new Set(['hasAttachments']);

// Numeric fields use numeric operators and a number value.
export const numericFields = new Set(['spamScore']);

export const booleanValueOptions: { value: string; label: string }[] = [
  { value: 'true', label: 'Yes' },
  { value: 'false', label: 'No' },
];

export const numericOperatorOptions: { value: string; label: string }[] = [
  { value: 'gt', label: 'greater than (>)' },
  { value: 'gte', label: 'greater or equal (≥)' },
  { value: 'lt', label: 'less than (<)' },
  { value: 'lte', label: 'less or equal (≤)' },
  { value: 'eq', label: 'equals (=)' },
  { value: 'neq', label: 'not equals (≠)' },
];

// "Then apply" action types. "drop" discards the email (no ticket created).
export const actionTypeOptions: { value: string; label: string }[] = [
  { value: 'tag', label: 'Add tags' },
  { value: 'assign', label: 'Assign to user' },
  { value: 'status', label: 'Set status' },
  { value: 'priority', label: 'Set priority' },
  { value: 'drop', label: 'Drop (discard email)' },
];

export function isNumericField(field?: string): boolean {
  return !!field && numericFields.has(field);
}

export const operatorOptions: { value: string; label: string }[] = [
  { value: 'contains', label: 'contains' },
  { value: 'not_contains', label: 'does not contain' },
  { value: 'equals', label: 'equals' },
  { value: 'not_equals', label: 'does not equal' },
  { value: 'starts_with', label: 'starts with' },
  { value: 'ends_with', label: 'ends with' },
  { value: 'matches', label: 'matches regex' },
];

export const matchOptions: { value: string; label: string }[] = [
  { value: 'ALL', label: 'All conditions (AND)' },
  { value: 'ANY', label: 'Any condition (OR)' },
];

export const tagKindOptions: { value: string; label: string }[] = [
  { value: 'TAG', label: 'Tag' },
  { value: 'CATEGORY', label: 'Category' },
];

export function fieldLabel(v?: string): string {
  return fieldOptions.find((o) => o.value === v)?.label ?? v ?? '';
}

export function operatorLabel(v?: string): string {
  return (
    operatorOptions.find((o) => o.value === v)?.label ??
    numericOperatorOptions.find((o) => o.value === v)?.label ??
    v ??
    ''
  );
}

// Deterministic fallback colour for a tag with no explicit colour.
const PALETTE = [
  'blue',
  'green',
  'orange',
  'purple',
  'cyan',
  'magenta',
  'gold',
  'geekblue',
];

export function tagColor(color?: string, name?: string): string {
  if (color) return color;
  const key = name ?? '';
  let hash = 0;
  for (let i = 0; i < key.length; i++) {
    hash = (hash * 31 + key.charCodeAt(i)) >>> 0;
  }
  return PALETTE[hash % PALETTE.length] ?? 'blue';
}
