import { defineStore } from 'pinia'
import { ref } from 'vue'
import { api, describe } from '@/api/client'
import type { BackupResult, TicketStats } from '@/api/types'

export const DEFAULT_DAYS = 30

/** Dashboard aggregates (stats:read) and the tenant backup (backup:manage). */
export const useStats = defineStore('ticket-stats', () => {
  const snapshot = ref<TicketStats | null>(null)
  const days = ref(DEFAULT_DAYS)
  const loading = ref(false)
  const error = ref('')

  async function load(d: number = days.value): Promise<void> {
    loading.value = true
    error.value = ''
    days.value = d
    try {
      snapshot.value = await api<TicketStats>('GET', 'stats', undefined, { query: { days: d } })
    } catch (e) {
      error.value = describe(e)
      snapshot.value = null
    } finally {
      loading.value = false
    }
  }

  const exportBackup = () => api<Record<string, unknown>>('POST', 'backup/export')
  const importBackup = (backup: unknown, mode: 'skip' | 'overwrite') => api<BackupResult>('POST', 'backup/import', { mode, backup })

  return { snapshot, days, loading, error, load, exportBackup, importBackup }
})
