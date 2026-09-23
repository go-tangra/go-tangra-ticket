import { z } from 'zod'
import { nonEmpty, optionalString } from '@freya/ui/forms'
import { TAG_COLORS } from '@/views/tags/colors'

/** POST/PUT /tags: the kind only matters on create (it is fixed afterwards). */
export const tagSchema = z.object({
  name: nonEmpty(100),
  kind: z.enum(['tag', 'category']).default('tag'),
  color: z.preprocess((v) => (v === '' || v === null ? undefined : v), z.enum(TAG_COLORS).optional()),
  description: optionalString(1000),
})
export type TagFormInput = z.output<typeof tagSchema>
