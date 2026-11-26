<script lang="ts" generics="T extends { value: string; name: string; detail?: string } = never">
  // The searchable target picker. By default it searches AllStar nodes on the server (api.search).
  // With `options` it searches a list the page already holds (reflector and master lists): the text stays
  // free-form, a pick only fills it in, and `label` says how an entry reads.
  import { api } from '../lib/api';
  import type { NodeInfo } from '../lib/types';
  type Opt = T;
  let { value = $bindable(''), placeholder = 'Node number, callsign or name', id, onenter, onpick, options, search, label, onpickoption, maxlength, inputmode = 'search', describedby, inputClass = '' }: {
    value: string; placeholder?: string; id?: string; onenter?: () => void; onpick?: (i: NodeInfo) => void;
    /** Local mode: the entries to search (an empty list never opens). */
    options?: Opt[];
    /** Local mode: filter the entries for the typed text (default: every word in name, value or detail). */
    search?: (text: string) => Opt[];
    /** Local mode: the first line of an entry. */
    label?: (o: Opt) => string;
    onpickoption?: (o: Opt) => void;
    /** Extra classes for the input. */
    inputClass?: string;
    maxlength?: number; inputmode?: 'search' | 'numeric' | 'text'; describedby?: string;
  } = $props();
  const local = $derived(options !== undefined);
  let results = $state<NodeInfo[]>([]);
  let localResults = $state<Opt[]>([]);
  let open = $state(false);
  let idx = $state(-1);
  let timer: ReturnType<typeof setTimeout> | undefined;
  let seq = 0;
  const listId = `np-${Math.random().toString(36).slice(2, 8)}`;
  const count = $derived(local ? localResults.length : results.length);

  function find(text: string): Opt[] {
    if (search) return search(text);
    const words = text.toLowerCase().split(/\s+/).filter(Boolean);
    return (options ?? []).filter((o) => words.every((w) => `${o.name} ${o.value} ${o.detail ?? ''}`.toLowerCase().includes(w))).slice(0, 50);
  }
  // Local mode: opening on focus lists the entries; the typed text only filters once it was edited.
  let edited = false;
  function refreshLocal() {
    localResults = find(edited ? value : '');
    open = localResults.length > 0;
    idx = -1;
  }
  function oninput() {
    if (local) { edited = true; refreshLocal(); return; }
    clearTimeout(timer);
    const text = value.trim();
    if (text.length < 2) { results = []; open = false; return; }
    timer = setTimeout(async () => {
      const my = ++seq;
      try { const r = await api.search(text, 8); if (my === seq) { results = r; open = r.length > 0; idx = -1; } } catch { /* ignore */ }
    }, 180);
  }
  function onfocus() { if (local) { edited = false; refreshLocal(); } }
  function pick(i: NodeInfo) { value = i.node; open = false; results = []; onpick?.(i); }
  function pickLocal(o: Opt) { value = o.value; open = false; localResults = []; onpickoption?.(o); }
  function onkey(e: KeyboardEvent) {
    if (e.key === 'ArrowDown' && !open && local) { e.preventDefault(); refreshLocal(); }
    else if (e.key === 'ArrowDown' && open) { e.preventDefault(); idx = (idx + 1) % count; }
    else if (e.key === 'ArrowUp' && open) { e.preventDefault(); idx = (idx - 1 + count) % count; }
    else if (e.key === 'Escape') open = false;
    else if (e.key === 'Enter') {
      if (open && idx >= 0) { e.preventDefault(); if (local) pickLocal(localResults[idx]); else pick(results[idx]); }
      else if (onenter) { e.preventDefault(); open = false; onenter(); }
    }
  }
</script>
<div class="relative">
  <input {id} class="input font-mono {inputClass}" role="combobox" aria-expanded={open} aria-controls={listId} aria-autocomplete="list" aria-activedescendant={idx >= 0 ? `${listId}-${idx}` : undefined} aria-describedby={describedby}
    {placeholder} {maxlength} {inputmode} bind:value {oninput} {onfocus} onkeydown={onkey} onblur={() => setTimeout(() => (open = false), 150)} autocomplete="off" autocapitalize="off" spellcheck="false" />
  {#if open}
    <ul id={listId} role="listbox" class="absolute z-20 mt-1 max-h-72 w-full overflow-auto rounded-lg border border-line bg-surface p-1 shadow-lg">
      {#if local}
        {#each localResults as o, i (i)}
          <li id="{listId}-{i}" role="option" aria-selected={i === idx}>
            <button type="button" tabindex="-1" class="flex w-full flex-col rounded-md px-2.5 py-1.5 text-left text-sm hover:bg-surface-2 {i === idx ? 'bg-surface-2' : ''}" onmousedown={(e) => { e.preventDefault(); pickLocal(o); }}>
              <span class="break-words font-medium">{label ? label(o) : o.name}</span>
              {#if o.detail}<span class="truncate text-xs text-muted">{o.detail}</span>{/if}
            </button>
          </li>
        {/each}
      {:else}
        {#each results as r, i (r.node)}
          <li id="{listId}-{i}" role="option" aria-selected={i === idx}>
            <button type="button" tabindex="-1" class="flex w-full flex-col rounded-md px-2.5 py-1.5 text-left text-sm hover:bg-surface-2 {i === idx ? 'bg-surface-2' : ''}" onmousedown={(e) => { e.preventDefault(); pick(r); }}>
              <span><span class="font-mono font-semibold">{r.node}</span> <span class="font-medium">{r.callsign}</span></span>
              <span class="truncate text-xs text-muted">{[r.description, r.location].filter(Boolean).join(' · ')}</span>
            </button>
          </li>
        {/each}
      {/if}
    </ul>
  {/if}
</div>
