<script setup lang="ts">
import { onMounted, ref, watch } from 'vue';

// Renders an email body in a sandboxed iframe (no script execution), HTML
// preferred and falling back to escaped plain text. Modeled on go-imap-admin's
// EmailViewer so email markup/CSS is isolated from the portal.
const props = defineProps<{ htmlContent?: string; textContent?: string }>();

const iframeRef = ref<HTMLIFrameElement | null>(null);
const height = ref('200px');

function esc(s: string): string {
  return s
    .replaceAll('&', '&amp;')
    .replaceAll('<', '&lt;')
    .replaceAll('>', '&gt;');
}

function htmlDoc(body: string): string {
  return `<!doctype html><html><head><meta charset="utf-8"><base target="_blank">
<style>
  html,body{margin:0;padding:10px;font-family:-apple-system,"Segoe UI",Roboto,Helvetica,Arial,sans-serif;font-size:14px;line-height:1.5;color:#1f2937;word-break:break-word}
  img{max-width:100%;height:auto}
  a{color:#2563eb}
  table{max-width:100%}
  blockquote{margin:0 0 0 8px;padding-left:10px;border-left:3px solid #d1d5db;color:#4b5563}
</style></head><body>${body}</body></html>`;
}

function textDoc(text: string): string {
  return `<!doctype html><html><head><meta charset="utf-8">
<style>body{margin:0;padding:10px;font-family:ui-monospace,Menlo,Consolas,monospace;font-size:13px;line-height:1.5;color:#1f2937;white-space:pre-wrap;word-break:break-word}</style>
</head><body>${esc(text)}</body></html>`;
}

function render() {
  const iframe = iframeRef.value;
  if (!iframe) return;
  const doc = iframe.contentDocument || iframe.contentWindow?.document;
  if (!doc) return;

  const html = (props.htmlContent || '').trim();
  const content = html ? htmlDoc(html) : textDoc(props.textContent || '(no body)');
  doc.open();
  doc.write(content);
  doc.close();

  // Auto-size to content (clamped).
  setTimeout(() => {
    try {
      const h = doc.body?.scrollHeight ?? 200;
      height.value = `${Math.min(Math.max(h + 16, 120), 800)}px`;
    } catch {
      // cross-origin guard — keep default
    }
  }, 60);
}

watch(() => [props.htmlContent, props.textContent], render);
onMounted(render);
</script>

<template>
  <iframe
    ref="iframeRef"
    title="Email body"
    sandbox="allow-same-origin allow-popups"
    :style="{
      width: '100%',
      height,
      border: '1px solid var(--border, #e5e7eb)',
      borderRadius: '6px',
      background: '#ffffff',
    }"
  ></iframe>
</template>
