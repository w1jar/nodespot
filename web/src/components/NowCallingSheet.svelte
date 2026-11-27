<script lang="ts">
  // The Now Calling details sheet. The caller passes the rows and links; `children` adds more (the app's Recent list).
  import type { Snippet } from 'svelte';
  import type { SheetRow } from '../lib/nowcalling';
  import X from '@lucide/svelte/icons/x';
  import ExternalLink from '@lucide/svelte/icons/external-link';
  let { open = $bindable(false), title, rows = [], links = [], children }: {
    open: boolean; title: string; rows?: SheetRow[]; links?: { label: string; href: string }[]; children?: Snippet;
  } = $props();
  let dlg: HTMLDialogElement | undefined = $state();
  $effect(() => {
    if (!dlg) return;
    if (open && !dlg.open) dlg.showModal();
    if (!open && dlg.open) dlg.close();
  });
</script>

<dialog bind:this={dlg} onclose={() => (open = false)} onclick={(e) => { if (e.target === dlg) open = false; }}
  aria-labelledby="nc-title"
  class="nc-sheet m-auto w-[calc(100%-1.5rem)] max-w-md rounded-xl border border-line bg-surface p-0 text-fg shadow-2xl backdrop:bg-black/50 max-sm:mb-0 max-sm:mt-auto max-sm:w-full max-sm:max-w-none max-sm:rounded-b-none">
  {#if open}
    <div class="flex items-center justify-between border-b border-line px-4 py-2">
      <h2 id="nc-title" class="text-base font-semibold">{title}</h2>
      <button class="btn btn-icon border-transparent min-h-11 min-w-11" aria-label="Close" onclick={() => (open = false)}><X size={18} /></button>
    </div>
    <div class="max-h-[75dvh] space-y-4 overflow-y-auto p-4 text-sm">
      {#if rows.length > 0}
        <dl class="grid grid-cols-[auto_minmax(0,1fr)] gap-x-3 gap-y-1.5">
          {#each rows as r, i (r.label + i)}
            <dt class="text-muted">{r.label}</dt>
            <dd class="break-words {r.cls ?? ''}">{r.value}{#if r.note}<span class="ml-1 text-xs text-muted">{r.note}</span>{/if}</dd>
          {/each}
        </dl>
      {/if}
      {#if links.length > 0}
        <div class="flex flex-wrap gap-2">
          {#each links as l, i (l.label + i)}<a class="btn min-h-11" href={l.href} target="_blank" rel="noopener noreferrer">{l.label}<ExternalLink size={14} /></a>{/each}
        </div>
      {/if}
      {@render children?.()}
    </div>
  {/if}
</dialog>
