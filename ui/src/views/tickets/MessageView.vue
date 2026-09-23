<script setup lang="ts">
// The ticket's first message. HTML mail is shown only as the server-sanitised
// rendition, inside a sandboxed srcdoc iframe with NO sandbox permissions (no
// scripts, opaque origin) — the edge CSP applies as a second layer. Inline
// images (the server rewrites cid: to this module's attachment route) are
// fetched here and embedded as data: URIs, so the opaque-origin frame loads
// nothing itself. A toggle shows the plain-text body; attachments download
// through the tenant-checked route.
import { computed, ref, watch } from 'vue'
import { UiAlert, UiButton, UiIcon, UiSkeleton } from '@freya/ui'
import { useTickets } from '@/stores/tickets'
import type { Attachment, Ticket } from '@/api/types'
import { BASE, describe, fileUrl } from '@/api/client'

const props = defineProps<{ ticket: Ticket }>()
const store = useTickets()

const html = ref('')
const text = ref('')
const loading = ref(false)
const error = ref('')
const plain = ref(false)

/** Same-module attachment URLs the sanitiser leaves in img src (cid: rewrites). */
const ATTACHMENT_SRC = new RegExp('src="(' + BASE.replace(/[/]/g, '\\/') + '\\/tickets\\/[A-Za-z0-9-]+\\/attachments\\/[A-Za-z0-9-]+)"', 'g')

const IMAGE_TYPES = ['image/png', 'image/jpeg', 'image/gif', 'image/webp']

function base64(buf: ArrayBuffer): string {
  const bytes = new Uint8Array(buf)
  let bin = ''
  for (let i = 0; i < bytes.length; i += 0x8000) bin += String.fromCharCode(...bytes.subarray(i, i + 0x8000))
  return btoa(bin)
}

/** Replaces each attachment image URL with a data: URI (unloadable ones are dropped). */
async function embedImages(src: string): Promise<string> {
  const urls = [...new Set([...src.matchAll(ATTACHMENT_SRC)].map((m) => m[1]!))]
  let out = src
  for (const u of urls) {
    let data = ''
    try {
      const res = await fetch(u, { credentials: 'same-origin' })
      const type = (res.headers.get('Content-Type') ?? '').split(';')[0]!.trim().toLowerCase()
      if (res.ok && IMAGE_TYPES.includes(type)) data = 'data:' + type + ';base64,' + base64(await res.arrayBuffer())
    } catch {
      data = ''
    }
    out = out.split('src="' + u + '"').join('src="' + data + '"')
  }
  return out
}

async function load(): Promise<void> {
  html.value = ''
  text.value = props.ticket.description ?? ''
  error.value = ''
  plain.value = false
  if (!props.ticket.has_html) return
  loading.value = true
  try {
    const b = await store.body(props.ticket.id)
    text.value = b.text || text.value
    html.value = b.html_sanitized ? await embedImages(b.html_sanitized) : ''
  } catch (e) {
    error.value = describe(e)
  } finally {
    loading.value = false
  }
}
watch(() => props.ticket.id, () => void load(), { immediate: true })

const showHtml = computed(() => !!html.value && !plain.value)
const files = computed<Attachment[]>(() => props.ticket.attachments ?? [])
const downloadUrl = (a: Attachment) => fileUrl('tickets/' + props.ticket.id + '/attachments/' + a.id)
function size(n: number): string {
  if (n < 1024) return n + ' B'
  if (n < 1024 * 1024) return (n / 1024).toFixed(1) + ' KiB'
  return (n / 1024 / 1024).toFixed(1) + ' MiB'
}
</script>

<template>
  <section class="mb-4" data-test="message-view">
    <div class="mb-2 flex items-center gap-2">
      <span class="text-sm font-medium">Message</span>
      <span class="grow" />
      <UiButton v-if="html" size="xs" variant="text" icon="mdi-file-document-outline" :aria-pressed="plain" data-test="message-plain-toggle" @click="plain = !plain">{{ plain ? 'Formatted' : 'Plain text' }}</UiButton>
    </div>
    <UiAlert v-if="error" kind="warning" class="mb-2">{{ error }}</UiAlert>
    <UiSkeleton v-if="loading" :lines="4" />
    <iframe v-else-if="showHtml" sandbox="" referrerpolicy="no-referrer" title="Email message" class="h-96 w-full rounded-box border border-base-300 bg-white" :srcdoc="html" data-test="message-frame" />
    <p v-else-if="text" class="rounded-box border border-base-300 p-3 text-sm whitespace-pre-wrap break-words" data-test="message-text">{{ text }}</p>
    <p v-else class="text-sm text-base-content/70" data-test="message-empty">No message body.</p>

    <ul v-if="files.length" class="mt-3 flex flex-col gap-1" data-test="message-attachments">
      <li v-for="a in files" :key="a.id" class="flex items-center gap-2 text-sm" :data-test="'attachment-' + a.id">
        <UiIcon name="mdi-paperclip" size="sm" />
        <span class="min-w-0 truncate">{{ a.filename }}</span>
        <span class="text-xs text-base-content/70">{{ size(a.size) }}</span>
        <span class="grow" />
        <a class="btn btn-xs btn-text" :href="downloadUrl(a)" :download="a.filename" rel="noopener" :aria-label="'Download ' + a.filename"><UiIcon name="mdi-download" size="sm" /></a>
      </li>
    </ul>
  </section>
</template>
