import { ApiError, describe } from '@/api/client'

/** A rules refusal as text: invalid_rule carries the field and the reason. */
export function ruleErrorText(e: unknown): string {
  if (e instanceof ApiError && e.reason === 'invalid_rule' && e.detail) {
    const field = typeof e.detail.field === 'string' ? e.detail.field : ''
    const message = typeof e.detail.message === 'string' ? e.detail.message : ''
    if (message) return (field ? field + ': ' : '') + message
  }
  return describe(e)
}
