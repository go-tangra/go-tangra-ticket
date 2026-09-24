// Tag colours are UI palette names (the badge colours of the kit), so chips
// never need inline styles. A tag saved without a colour (or with a legacy
// #rrggbb value from an import) gets a stable automatic colour derived from its
// name: the same name always renders the same colour.
import type { Tag } from '@/api/types'

export const TAG_COLORS = ['primary', 'secondary', 'accent', 'info', 'success', 'warning', 'error', 'neutral'] as const
export type TagColor = (typeof TAG_COLORS)[number]

export const TAG_COLOR_LABELS: Record<TagColor, string> = {
  primary: 'Primary', secondary: 'Secondary', accent: 'Accent', info: 'Blue', success: 'Green', warning: 'Amber', error: 'Red', neutral: 'Grey',
}

export function isTagColor(c: unknown): c is TagColor {
  return typeof c === 'string' && (TAG_COLORS as readonly string[]).includes(c)
}

/** Deterministic palette colour for a name (FNV-1a over the lower-cased name). */
export function autoColor(name: string): TagColor {
  let h = 0x811c9dc5
  for (const ch of name.trim().toLowerCase()) {
    h ^= ch.codePointAt(0) ?? 0
    h = Math.imul(h, 0x01000193) >>> 0
  }
  // neutral is left for explicit choice: automatic colours stay distinguishable
  return TAG_COLORS[h % (TAG_COLORS.length - 1)]!
}

/** The colour a tag renders with. */
export function tagColor(tag: Pick<Tag, 'name' | 'color'>): TagColor {
  return isTagColor(tag.color) ? tag.color : autoColor(tag.name)
}
