import { z } from 'zod'

export const BACKUP_MODES = ['skip', 'overwrite'] as const
export const BACKUP_MODE_LABELS: Record<(typeof BACKUP_MODES)[number], string> = { skip: 'Keep existing rows', overwrite: 'Overwrite existing rows' }

/** Import form: a JSON file plus the conflict mode (the file is parsed before the request). */
export const backupImportSchema = z.object({
  file: z.instanceof(File, { message: 'Choose a backup file.' }).refine((f) => f.size <= 64 * 1024 * 1024, 'The file is larger than 64 MiB.'),
  mode: z.enum(BACKUP_MODES),
})
export type BackupImportInput = z.output<typeof backupImportSchema>

/** Dashboard windows (days); the API accepts 1–365. */
export const STATS_WINDOWS = [7, 30, 90, 365] as const
