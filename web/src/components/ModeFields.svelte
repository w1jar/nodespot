<script lang="ts">
  // The settings form of one mode, drawn from the fields the server lists. A password field stays empty and shows "set"
  // when a value is saved; leave it empty to keep it.
  import { api } from '../lib/api';
  import { stationHint, stationValue, type StationValues } from '../lib/modes';
  import { masterHint, masterListNote, masterOptions, masterPick, masterSearch, type MasterOption } from '../lib/hostlists';
  import NodePicker from './NodePicker.svelte';
  import type { HostItem, ModeInfo } from '../lib/types';
  import Confirm from './Confirm.svelte';
  let { mode, draft = $bindable(), idPrefix = 'mf', busy = false, onclear }: { mode: ModeInfo; draft: Record<string, string>; idPrefix?: string; busy?: boolean; onclear?: (key: string) => void } = $props();
  const fields = $derived(mode.fields ?? []);
  const basic = $derived(fields.filter((f) => !f.advanced));
  const adv = $derived(fields.filter((f) => f.advanced));
  const id = (k: string) => `${idPrefix}-${mode.name}-${k}`;
  // The station values the empty fields fall back to; shown as placeholders.
  let st = $state<StationValues | undefined>(undefined);
  $effect(() => { if (fields.some((f) => f.stationDefault)) api.station().then((s) => { st = { callsign: s.callsign, dmrId: s.dmrId, nxdnId: s.nxdnId }; }).catch(() => { /* the node checks */ }); });
  // Fields with a host list (BrandMeister and DMR masters) are a type-to-search combobox: typing filters the list by name,
  // host or country, arrow keys and Enter pick, and any other text (host or host:port) is kept as typed.
  // undefined: not loaded yet; []: the list is not downloaded, so the field shows with a note.
  let lists = $state<Record<string, HostItem[] | undefined>>({});
  const asked = new Set<string>();
  $effect(() => {
    for (const f of fields) {
      const k = f.hostList;
      if (!k || asked.has(k)) continue;
      asked.add(k);
      api.hostLists(k).then((h) => { lists[k] = h.items; }).catch(() => { lists[k] = []; });
    }
  });
  function pickMaster(f: (typeof fields)[number], o: MasterOption) {
    const ch = masterPick(f.hostList as 'bm' | 'dmr-other', o.item, draft, !!mode.secretsSet?.password);
    draft[f.key] = ch.address;
    if (ch.password !== undefined) draft.password = ch.password;
  }
  // The saved password the Clear button would delete, after the confirm dialog.
  let clearKey = $state<string | null>(null), clearOpen = $state(false);
  const clearLabel = $derived(fields.find((x) => x.key === clearKey)?.label ?? 'password');
  const chk = 'size-4 accent-[var(--accent)]';
</script>

{#snippet field(f: (typeof fields)[number])}
  {#if f.kind === 'bool'}
    <label class="flex cursor-pointer items-start gap-2 text-sm sm:col-span-2"><input type="checkbox" class="mt-0.5 {chk}" checked={draft[f.key] === 'true'} onchange={(e) => (draft[f.key] = e.currentTarget.checked ? 'true' : 'false')} /><span>{f.label}{#if f.help}<span class="block text-xs text-muted">{f.help}</span>{/if}</span></label>
  {:else}
    <div>
      <label class="label" for={id(f.key)}>{f.label}{#if f.required && !f.stationDefault}<span class="text-danger" aria-hidden="true"> *</span>{/if}</label>
      {#if f.kind === 'select'}
        <select id={id(f.key)} class="input" bind:value={draft[f.key]}>{#each f.options ?? [] as o}<option value={o}>{o}</option>{/each}</select>
      {:else if (f.kind === 'password' || f.secret)}
        <input id={id(f.key)} class="input" type="password" autocomplete="new-password" bind:value={draft[f.key]} placeholder={mode.secretsSet?.[f.key] ? 'set' : ''} required={f.required && !mode.secretsSet?.[f.key]} />
      {:else if f.hostList && lists[f.hostList]?.length}
        {@const kind = f.hostList as 'bm' | 'dmr-other'}
        {@const items = lists[f.hostList] ?? []}
        {@const opts = masterOptions(kind, items)}
        {@const hint = masterHint(kind, draft[f.key] ?? '', items)}
        <NodePicker id={id(f.key)} bind:value={draft[f.key]} options={opts} search={(t) => masterSearch(opts, t)} onpickoption={(o) => pickMaster(f, o)}
          placeholder="Search name, host or country, or type host:port" inputmode="text" testid="master-combo" describedby="{id(f.key)}-hint" />
        {#if hint}<p id="{id(f.key)}-hint" class="mt-1 text-xs text-muted" data-testid="master-hint">{hint}</p>{/if}
      {:else}
        <input id={id(f.key)} class="input {f.kind === 'number' ? 'tabular-nums' : ''}" type={f.kind === 'number' ? 'number' : 'text'} autocomplete="off" bind:value={draft[f.key]} placeholder={f.stationDefault ? stationValue(f, st) : f.default ?? ''} required={f.required && !f.stationDefault} />
      {/if}
      {#if f.help}<p class="mt-1 text-xs text-muted">{f.help}</p>{/if}
      {#if f.hostList && lists[f.hostList] && !lists[f.hostList]?.length}<p class="mt-1 text-xs text-warn" data-testid="master-note">{masterListNote(f.hostList)}</p>{/if}
      {#if f.stationDefault}<p class="mt-1 text-xs text-muted">Leave empty to use the station value ({stationHint(f, st)}).</p>{/if}
      {#if (f.kind === 'password' || f.secret) && mode.secretsSet?.[f.key]}
        <p class="mt-1 flex flex-wrap items-center gap-2 text-xs text-muted">Saved. Leave empty to keep it.
          {#if onclear}<button type="button" class="btn btn-sm btn-bad-outline" disabled={busy} data-testid="clear-secret-{f.key}" onclick={() => { clearKey = f.key; clearOpen = true; }}>Clear</button>{/if}
        </p>
      {/if}
    </div>
  {/if}
{/snippet}

<div class="grid gap-3 sm:grid-cols-2">
  {#each basic as f (f.key)}{@render field(f)}{/each}
</div>
{#if adv.length}
  <details class="mt-3 rounded border border-line">
    <summary class="cursor-pointer px-2 py-1 text-sm font-medium">Advanced</summary>
    <div class="grid gap-3 p-2 sm:grid-cols-2">{#each adv as f (f.key)}{@render field(f)}{/each}</div>
  </details>
{/if}
<Confirm bind:open={clearOpen} title="Clear {clearLabel}?" message="The saved {clearLabel.toLowerCase()} is removed from {mode.title || mode.name}. Other settings stay. {mode.title || mode.name} will not be ready until you enter a new one." confirmLabel="Clear" onconfirm={() => { if (clearKey) onclear?.(clearKey); }} />
