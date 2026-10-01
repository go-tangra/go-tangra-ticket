// Shared state of one server-paged list (go-tangra specs/032-server-side-tables):
// the current page of rows and its total, loaded with the page/size/sort the
// view's useListQuery supplies plus list filters. Only the latest request's
// answer is applied.
import { ref, type Ref } from 'vue'
import type { ListParams } from '@go-tangra/ui'
import { api, describe } from '@/api/client'
import type { Page } from '@/api/types'

/** Page/size/sort from useListQuery plus the list's filters. */
export type ListQueryParams = Partial<ListParams> & { kind?: string | undefined }

export function pagedList<T>(path: string) {
  const items = ref([]) as Ref<T[]>
  const total = ref(0)
  const loading = ref(false)
  const error = ref('')
  const params = ref<ListQueryParams>({})
  let seq = 0

  /** Loads one page; resolves with the page the server answered (it clamps), or null when superseded or failed. */
  async function list(p: ListQueryParams = params.value): Promise<number | null> {
    const mine = ++seq
    params.value = { ...p }
    loading.value = true
    error.value = ''
    try {
      const res = await api<Page<T>>('GET', path, undefined, { query: { ...p } })
      if (mine !== seq) return null
      items.value = res.items ?? []
      total.value = res.total ?? 0
      return res.page ?? 1
    } catch (e) {
      if (mine === seq) error.value = describe(e)
      return null
    } finally {
      if (mine === seq) loading.value = false
    }
  }

  /** Reloads the current page (same parameters). */
  const reload = () => list(params.value)

  return { items, total, loading, error, params, list, reload }
}
