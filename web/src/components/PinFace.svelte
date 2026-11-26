<script lang="ts">
  // The inside of a one-tap favorite button (class `btn btn-pin`), shared by the Dashboard's Pinned buttons
  // and the Modes page favorites: the green dot when active, an optional tag, the name and the mono target.
  let { active = false, tag = '', label, sub = '', dotId }: { active?: boolean; tag?: string; label: string; sub?: string; dotId?: string } = $props();
</script>
{#if active}<span class="inline-block size-2 shrink-0 rounded-full bg-ok" data-testid={dotId} aria-hidden="true"></span><span class="sr-only">active</span>{/if}
{#if tag}<span class="rounded bg-surface-2 px-1 text-[10px] font-semibold text-muted">{tag}</span>{/if}
<span class="truncate">{label}</span>
{#if sub}<span class="font-mono text-xs text-muted">{sub}</span>{/if}
