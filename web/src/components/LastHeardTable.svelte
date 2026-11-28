<script lang="ts">
  // The Last heard table of the Dashboard, the DV tab and the digital modes card: Callsign, Mode, Target, Time (right).
  // A callsign with something to look up is a button that opens a small menu (QRZ, radioid.net); Escape, an outside click,
  // a scroll or tabbing away closes it. The menu is fixed to the viewport so the table's horizontal scroll cannot clip it.
  import { tick } from 'svelte';
  import ExternalLink from '@lucide/svelte/icons/external-link';
  import { clock } from '../lib/store.svelte';
  import { agoText } from '../lib/nowcalling';
  import type { HeardItem } from '../lib/lastheard';

  let { rows, testid = undefined }: { rows: HeardItem[]; testid?: string } = $props();

  const MENU_W = 208, MARGIN = 8;
  let openKey = $state(''), pos = $state({ left: 0, top: 0 });
  let menuEl = $state<HTMLDivElement>(), trigger: HTMLElement | null = null;
  const current = $derived(rows.find((r) => r.key === openKey));

  async function toggle(r: HeardItem, e: MouseEvent) {
    if (openKey === r.key) { close(false); return; }
    trigger = e.currentTarget as HTMLElement;
    const b = trigger.getBoundingClientRect(), w = Math.min(MENU_W, window.innerWidth - 2 * MARGIN);
    const h = 2 * 44 + 3 * 4 + 2;
    pos = {
      left: Math.max(MARGIN, Math.min(b.left, window.innerWidth - w - MARGIN)),
      top: b.bottom + 4 + h > window.innerHeight && b.top - 4 - h > 0 ? b.top - 4 - h : b.bottom + 4,
    };
    openKey = r.key;
    await tick();
    menuEl?.querySelector<HTMLElement>('a')?.focus();
  }
  function close(refocus: boolean) {
    openKey = '';
    if (refocus) trigger?.focus();
  }
  function onKey(e: KeyboardEvent) {
    if (openKey && e.key === 'Escape') { e.stopPropagation(); close(true); }
  }
  function onPointer(e: PointerEvent) {
    if (!openKey) return;
    const t = e.target as Node;
    if (menuEl?.contains(t) || trigger?.contains(t)) return;
    close(false);
  }
  function onFocusOut(e: FocusEvent) {
    const to = e.relatedTarget as Node | null;
    if (to && !menuEl?.contains(to) && !trigger?.contains(to)) close(false);
  }
  // The menu is fixed, so any scroll (the page or the table) would leave it behind: close it.
  $effect(() => {
    if (!openKey) return;
    const t0 = Date.now();
    const f = () => { if (Date.now() - t0 > 300) close(false); }; // a scroll that brought the button into view is not a reason to close
    window.addEventListener('scroll', f, true);
    return () => window.removeEventListener('scroll', f, true);
  });
  const when = (at: number) => (at ? agoText((clock.now - at) / 1000) : '');
</script>

<svelte:window onkeydown={onKey} onpointerdown={onPointer} onresize={() => close(false)} />

<div class="overflow-x-auto" data-testid={testid}>
  <table class="w-full text-left text-xs sm:text-sm">
    <thead class="text-muted">
      <tr>
        <th class="py-1 pr-3 font-medium">Callsign</th>
        <th class="hidden py-1 pr-3 font-medium sm:table-cell">Mode</th>
        <th class="py-1 pr-3 font-medium">Target</th>
        <th class="py-1 text-right font-medium">Time</th>
      </tr>
    </thead>
    <tbody class="divide-y divide-line">
      {#each rows as r, i (r.key + i)}
        <tr title={r.title}>
          <td class="whitespace-nowrap pr-3 font-mono">
            {#if r.qrz || r.radioid}
              <button type="button" class="-ml-2 inline-flex min-h-11 items-center rounded px-2 font-mono text-accent underline decoration-dotted underline-offset-2 hover:bg-surface-2" aria-haspopup="true" aria-expanded={openKey === r.key} aria-label="{r.call}: look up" onclick={(e) => toggle(r, e)}>{r.call}</button>
            {:else}<span class="inline-flex min-h-11 items-center">{r.call}</span>{/if}{#if r.via}<span class="badge ml-1 font-sans" data-testid="heard-via" title="Heard on the radio, not from the network">{r.via}</span>{/if}{#if r.count && r.count > 1}<span class="ml-1 font-sans text-xs text-muted" data-testid="heard-count" title="{r.count} transmissions">×{r.count}</span>{/if}
            {#if r.person}<span class="-mt-2.5 mb-1 block max-w-[14rem] truncate font-sans text-xs text-muted sm:max-w-xs" title={r.person} data-testid="heard-person">{r.person}</span>{/if}
          </td>
          <td class="hidden pr-3 sm:table-cell">{r.mode}</td>
          <td class="pr-3 font-mono">{r.target}</td>
          <td class="whitespace-nowrap text-right text-muted">{when(r.at)}</td>
        </tr>
      {/each}
    </tbody>
  </table>
</div>

{#if current}
  <!-- svelte-ignore a11y_no_noninteractive_element_interactions -->
  <div bind:this={menuEl} role="group" aria-label="Look up {current.call}" tabindex="-1" class="fixed z-50 flex flex-col gap-1 rounded-md border border-line bg-surface p-1 shadow-lg" style="left:{pos.left}px;top:{pos.top}px;width:min({MENU_W}px, calc(100vw - {2 * MARGIN}px))" onfocusout={onFocusOut}>
    {#if current.qrz}<a class="btn min-h-11" href={current.qrz} target="_blank" rel="noopener noreferrer" onclick={() => close(false)}>QRZ lookup<ExternalLink size={14} /></a>{/if}
    {#if current.radioid}<a class="btn min-h-11" href={current.radioid} target="_blank" rel="noopener noreferrer" onclick={() => close(false)}>radioid.net<ExternalLink size={14} /></a>{/if}
  </div>
{/if}
