<script lang="ts">
  // The settings form of one mode, drawn from the fields the server lists. A password field stays empty and shows "set"
  // when a value is saved; leave it empty to keep it.
  import { api } from '../lib/api';
  import { stationHint, stationValue, type StationValues } from '../lib/modes';
  import { MASTER_OTHER, masterChoice, masterLabel, masterListNote, masterPick } from '../lib/hostlists';
  import type { HostItem, ModeInfo } from '../lib/types';
  let { mode, draft = $bindable(), idPrefix = 'mf' }: { mode: ModeInfo; draft: Record<string, string>; idPrefix?: string } = $props();
  const fields = $derived(mode.fields ?? []);
  const basic = $derived(fields.filter((f) => !f.advanced));
  const adv = $derived(fields.filter((f) => f.advanced));
  const id = (k: string) => `${idPrefix}-${mode.name}-${k}`;
  // The station values the empty fields fall back to; shown as placeholders.
  let st = $state<StationValues | undefined>(undefined);
  $effect(() => { if (fields.some((f) => f.stationDefault)) api.station().then((s) => { st = { callsign: s.callsign, dmrId: s.dmrId, nxdnId: s.nxdnId }; }).catch(() => { /* the node checks */ }); });
  // Fields with a host list (BrandMeister and DMR masters) are a dropdown of the list plus "Other…", which shows a text field.
  // undefined: not loaded yet; []: the list is not downloaded, so the text field shows with a note.
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
  let other = $state<Record<string, boolean>>({});
  const okey = (key: string) => `${mode.name}.${key}`;
  const choice = (f: (typeof fields)[number], items: HostItem[]) => (other[okey(f.key)] ? MASTER_OTHER : masterChoice(f.hostList!, draft[f.key] ?? '', items));
  function pickMaster(f: (typeof fields)[number], items: HostItem[], v: string) {
    const k = f.hostList!;
    if (v === MASTER_OTHER) {
      if (masterChoice(k, draft[f.key] ?? '', items) !== MASTER_OTHER) draft[f.key] = '';
      other[okey(f.key)] = true;
      return;
    }
    other[okey(f.key)] = false;
    const it = items.find((x) => x.value === v);
    if (!it) { draft[f.key] = ''; return; }
    const ch = masterPick(k, it, draft, !!mode.secretsSet?.password);
    draft[f.key] = ch.address;
    if (ch.password !== undefined) draft.password = ch.password;
  }
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
        {@const items = lists[f.hostList] ?? []}
        {@const sel = choice(f, items)}
        <select id={id(f.key)} class="input" value={sel} required={f.required} onchange={(e) => pickMaster(f, items, e.currentTarget.value)} data-testid="master-select">
          {#if sel === ''}<option value="">Select a master…</option>{/if}
          {#each items as it (it.value)}<option value={it.value}>{masterLabel(f.hostList, it)}</option>{/each}
          <option value={MASTER_OTHER}>Other…</option>
        </select>
        {#if sel === MASTER_OTHER}
          <input class="input mt-2" type="text" autocomplete="off" autocapitalize="off" spellcheck="false" aria-label="{f.label}: host or host:port" placeholder="host or host:port" bind:value={draft[f.key]} required={f.required} data-testid="master-other" />
        {/if}
      {:else}
        <input id={id(f.key)} class="input {f.kind === 'number' ? 'tabular-nums' : ''}" type={f.kind === 'number' ? 'number' : 'text'} autocomplete="off" bind:value={draft[f.key]} placeholder={f.stationDefault ? stationValue(f, st) : f.default ?? ''} required={f.required && !f.stationDefault} />
      {/if}
      {#if f.help}<p class="mt-1 text-xs text-muted">{f.help}</p>{/if}
      {#if f.hostList && lists[f.hostList] && !lists[f.hostList]?.length}<p class="mt-1 text-xs text-warn" data-testid="master-note">{masterListNote(f.hostList)}</p>{/if}
      {#if f.stationDefault}<p class="mt-1 text-xs text-muted">Leave empty to use the station value ({stationHint(f, st)}).</p>{/if}
      {#if (f.kind === 'password' || f.secret) && mode.secretsSet?.[f.key]}<p class="mt-1 text-xs text-muted">Saved. Leave empty to keep it.</p>{/if}
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
