<script lang="ts">
  // Modes: one row of buttons for every mode (AllStar, EchoLink, BM, DMR, TGIF, YSF, P25, NXDN, M17, echo test). A button
  // shows that mode below; making it the node's active mode is a separate, explicit action. Below: its status, the connect
  // box (with lists or a node search), favorites with pins for the dashboard, and the mode's settings, inline.
  import { api } from '../lib/api';
  import { router } from '../lib/router.svelte';
  import { app, canOperate, isAdmin, refreshFavorites, refreshModes } from '../lib/store.svelte';
  import { toast, toastError } from '../lib/toast.svelte';
  import {
    CONFIRM_MS, defaultFavName, favoriteActive, favoriteBlock, favoritesInOrder, findFavorite, hasTarget, linkStatsText, linkStatsTitle, m17Split, modeChip, modeTitle, movedIds,
    needsVocoder, notReadyShort, pendingOutcome, pickerKind, recentTargets, M17_MODULES, sortModes, stateText, targetBody, targetPlaceholder, targetText, targetValue,
    validFavName, vocoderBusy, targetLabel, ECHO_CODECS, echoCodec, echoCodecValues,
  } from '../lib/modes';
  import type { Pending } from '../lib/modes';
  import type { Favorite, HostItem, HostList, NodeInfo, Vocoder } from '../lib/types';
  import FavoriteDialog from './FavoriteDialog.svelte';
  import Confirm from './Confirm.svelte';
  import Dialog from './Dialog.svelte';
  import Footer from './Footer.svelte';
  import Menu from './Menu.svelte';
  import ModeSettings from './ModeSettings.svelte';
  import NodePicker from './NodePicker.svelte';
  import { emptyListNote } from '../lib/hostlists';
  import PinFace from './PinFace.svelte';
  import Plug from '@lucide/svelte/icons/plug';
  import Unplug from '@lucide/svelte/icons/unplug';
  import Pin from '@lucide/svelte/icons/pin';
  import Star from '@lucide/svelte/icons/star';
  import Check from '@lucide/svelte/icons/check';
  import Ellipsis from '@lucide/svelte/icons/ellipsis';
  import Search from '@lucide/svelte/icons/search';
  import Plus from '@lucide/svelte/icons/plus';
  import Mic from '@lucide/svelte/icons/mic';
  import ChevronDown from '@lucide/svelte/icons/chevron-down';
  import ChevronRight from '@lucide/svelte/icons/chevron-right';
  import Power from '@lucide/svelte/icons/power';

  const op = $derived(canOperate());
  const admin = $derived(isAdmin());
  const list = $derived(sortModes(app.modes));
  const active = $derived(app.node?.active ?? '');
  // The mode being looked at: #/modes?mode=ysf, else the active one, else the first ready one, until a button is pressed.
  let picked = $state('');
  $effect(() => { const m = new URLSearchParams(router.query).get('mode'); if (m && router.path === '/modes') picked = m; });
  const sel = $derived(picked && list.some((m) => m.name === picked) ? picked : active || list.find((m) => m.configured)?.name || list[0]?.name || '');
  const info = $derived(list.find((m) => m.name === sel));
  const isActive = $derived(!!sel && sel === active);
  const st = $derived(isActive ? app.node?.mode : info?.status);
  const linked = $derived(isActive && (st?.state === 'connected' || st?.state === 'connecting') && !!st?.target);
  const kind = $derived(pickerKind(info));
  const title = $derived(info?.title || modeTitle(sel));

  // ---- Activating a mode: highlighted at once, confirmed by the node status, "Not confirmed" after CONFIRM_MS.
  let busy = $state(false);
  let pending = $state<Pending | null>(null), now = $state(Date.now());
  let timer: ReturnType<typeof setTimeout> | undefined;
  const outcome = $derived(pendingOutcome(pending, active, now));
  $effect(() => { if (outcome === 'confirmed') { pending = null; clearTimeout(timer); } });
  $effect(() => () => clearTimeout(timer));
  function expect(m: string) {
    clearTimeout(timer);
    now = Date.now();
    pending = { mode: m, since: now };
    timer = setTimeout(() => { now = Date.now(); }, CONFIRM_MS + 50);
  }
  async function run(fn: () => Promise<unknown>, ok: string, expected?: string) {
    if (busy) return;
    busy = true;
    if (expected !== undefined && expected !== active) expect(expected);
    try { await fn(); toast(ok); } catch (e) { pending = null; toastError(e); } finally { busy = false; }
  }

  // ---- The connect box.
  let text = $state(''), module = $state('C'), pickedName = $state('');
  $effect(() => { void sel; text = ''; pickedName = ''; });
  const value = $derived(targetValue(sel, text, module));
  let hostLists = $state<Record<string, HostList>>({});
  const tried = new Set<string>();
  $effect(() => {
    const m = sel;
    if (kind !== 'hosts' || tried.has(m)) return;
    tried.add(m);
    api.hostLists(m).then((h) => { hostLists[m] = h; }).catch(() => { hostLists[m] = { mode: m, items: [], etag: '' }; });
  });
  const hosts = $derived(hostLists[sel]?.items ?? []);
  function pickHost(o: HostItem) {
    pickedName = o.name;
    const s = sel === 'm17' ? m17Split(o.value) : null;
    if (s) { text = s.reflector; module = s.module; }
  }
  function pickNode(i: NodeInfo) { pickedName = i.callsign || ''; }
  const connect = (t = value, name = pickedName) => {
    if (!t) { toast(sel === 'm17' ? 'Enter a reflector such as M17-XXX' : 'Enter a target', 'error'); return; }
    return run(async () => { await api.select(sel, targetBody(t, name ? { name } : {})); text = ''; }, `${title}: connecting to ${t}`, sel);
  };
  const activate = () => run(() => api.select(sel), `${title} is active`, sel);
  const startStop = () => (isActive ? run(() => api.select(''), 'Echo test stopped', '') : activate());
  const stop = () => run(() => api.select(''), 'No mode is active', '');
  const disconnect = () => run(() => api.disconnect(), 'Disconnected');
  async function tap(f: Favorite) { await connect(f.target, f.label !== f.target ? f.label : ''); }

  const recent = $derived(recentTargets(app.heard, app.nowcalling?.recent ?? [], sel).filter((t) => !app.favorites.some((f) => f.mode === sel && f.target === t)));
  const favs = $derived(favoritesInOrder(app.favorites, sel));

  // ---- Favorites: the star saves the typed target (else the one linked now); pin shows it on the dashboard.
  const current = $derived(value || (linked ? st?.target?.id ?? '' : ''));
  const currentName = $derived(value ? pickedName : st?.target?.name ?? '');
  const starred = $derived(current ? findFavorite(app.favorites, sel, current) : undefined);
  const block = $derived(favoriteBlock(app.favorites, sel, current));
  let starOpen = $state(false), starName = $state('');
  function openStar() { if (block) return; starName = currentName ? defaultFavName(current, currentName) : ''; starOpen = true; }
  const starOk = $derived(starName.trim() === '' || validFavName(starName) !== null);
  async function saveStar(e: Event) {
    e.preventDefault();
    if (!starOk) return;
    busy = true;
    try { await api.addFavorite({ mode: sel, target: current, label: starName.trim() || undefined }); await refreshFavorites(); toast('Favorite saved'); starOpen = false; } catch (x) { toastError(x); } finally { busy = false; }
  }
  let dlg = $state(false), editing = $state<Favorite | null>(null), delFav = $state<Favorite | null>(null), delOpen = $state(false);
  async function pin(f: Favorite) {
    try { await api.updateFavorite(f.id, { pinned: !f.pinned }); await refreshFavorites(); toast(f.pinned ? 'Removed from the dashboard' : 'Pinned to the dashboard'); } catch (e) { toastError(e); }
  }
  async function move(f: Favorite, delta: number) {
    const ids = movedIds(favs, f.id, delta);
    if (!ids) return;
    try { await api.reorder(sel, ids); await refreshFavorites(); } catch (e) { toastError(e); }
  }
  async function remove() {
    const f = delFav; if (!f) return;
    try { await api.deleteFavorite(f.id); await refreshFavorites(); toast('Favorite removed'); } catch (e) { toastError(e); }
  }
  $effect(() => { if (!app.modes.length) refreshModes().catch(() => {}); });

  let voc = $state<Vocoder | null>(null);
  // ---- The echo test's codec: saved at once, used by the next recording (the node restarts the echo test on a save).
  let codecBusy = $state(false);
  const codec = $derived(echoCodec(info));
  const ambeNote = $derived(admin && sel === 'echotest' && codec === 'ambe' && needsVocoder(info, voc));
  async function setCodec(ev: Event) {
    const el = ev.currentTarget as HTMLSelectElement, v = el.value;
    if (codecBusy || v === codec) return;
    codecBusy = true;
    try {
      await api.saveMode('echotest', { values: echoCodecValues(info, v) });
      await refreshModes();
      toast(`Echo test codec: ${ECHO_CODECS.find((o) => o.value === v)?.label ?? v}. The next recording uses it.`);
    } catch (e) { el.value = codec; toastError(e); } finally { codecBusy = false; }
  }
  // ---- The settings section: collapsible; a mode that is not ready opens it.
  let openFor = $state<Record<string, boolean>>({});
  const vocNeeded = $derived(admin && needsVocoder(info, voc));
  const settingsOpen = $derived(openFor[sel] ?? (!!info && (!info.configured || vocNeeded)));

  // The AMBE vocoder (md380-emu): an install is a helper job; poll until it ends.
  async function loadVocoder() { try { voc = await api.vocoder(); } catch { /* the notice stays hidden when the status is unknown */ } }
  $effect(() => { if (admin) void loadVocoder(); });
  $effect(() => {
    if (!vocoderBusy(voc)) return;
    const t = setInterval(async () => {
      await loadVocoder();
      if (voc?.job === 'done') { toast('AMBE vocoder installed'); void refreshModes().catch(() => {}); }
      else if (voc?.job === 'failed') toast(voc.jobLog ? `Vocoder install failed: ${voc.jobLog.trim().split('\n').pop()}` : 'Vocoder install failed', 'error');
    }, 2000);
    return () => clearInterval(t);
  });

  const DOT = { ok: 'bg-ok', err: 'bg-danger', warn: 'bg-warn', off: 'bg-muted' } as const;
  const TXT = { ok: 'text-ok', err: 'text-danger', warn: 'text-warn', off: 'text-muted' } as const;
  const talker = $derived(isActive ? st?.talker : undefined);
  /** A local station is transmitting into the receiver and the node sends its audio to this (active) mode. */
  const localTx = $derived(isActive && !!app.node?.radio.rxActive);
  const levels = $derived(st?.levels ?? (sel === 'echotest' ? app.node?.echoTest : undefined));
</script>

<div class="space-y-3">
  {#if !app.node}
    <p class="py-10 text-center text-sm text-muted" role="status">Loading…</p>
  {:else}
    <section class="card space-y-3 p-3" aria-label="Modes" data-testid="modes">
      <div class="flex flex-wrap items-baseline gap-x-3 gap-y-0.5">
        <h2 class="text-base font-semibold">Modes</h2>
        <span class="min-w-0 text-xs text-muted" data-testid="active-line">{active ? `Active: ${modeTitle(active, app.modes)}` : 'No mode is active. The radio is idle.'}</span>
      </div>
      <div class="flex flex-wrap gap-1.5" role="group" aria-label="Choose a mode" data-testid="mode-buttons">
        {#each list as m (m.name)}
          {@const c = modeChip(m, active, app.node.mode?.state)}
          {@const on = m.name === sel}
          <button type="button" class="btn !min-h-10 min-w-0 grow gap-1.5 !px-2.5 sm:grow-0 {on ? 'border-accent bg-accent/10 ring-1 ring-accent' : ''}" aria-pressed={on} data-mode={m.name} data-active={m.name === active}
            title={m.configured ? '' : notReadyShort(m)} onclick={() => (picked = m.name)}>
            {#if m.name === active}<Check size={14} strokeWidth={3} class="shrink-0 text-ok" aria-hidden="true" />{/if}
            <span class="size-2 shrink-0 rounded-full {DOT[c.tone]}" aria-hidden="true"></span>
            <span class="font-medium">{m.title || modeTitle(m.name)}</span>
            <span class="text-xs {TXT[c.tone]}">{c.text}</span>
          </button>
        {/each}
      </div>
      {#if outcome === 'unconfirmed' && pending}
        <p class="flex flex-wrap items-center gap-2 text-sm text-warn" role="status" data-testid="not-confirmed">Not confirmed: the node still reports {active ? modeTitle(active, app.modes) : 'no mode'}, not {pending.mode ? modeTitle(pending.mode, app.modes) : 'idle'}.
          <button type="button" class="text-xs underline hover:no-underline" onclick={() => { pending = null; }}>Dismiss</button></p>
      {/if}
    </section>

    {#if info}
      <section class="card space-y-3 p-3" aria-label="{title} controls" data-testid="mode-panel" data-mode={sel}>
        <div class="flex flex-wrap items-center gap-2">
          <h2 class="text-base font-semibold">{title}</h2>
          {#if isActive}<span class="badge border-accent text-accent">Active</span>{/if}
          {#if isActive && st}<span class="badge {st.state === 'connected' ? 'text-ok' : st.state === 'error' ? 'text-danger' : 'text-warn'}">{stateText(st.state)}</span>{/if}
          {#if op && info.configured}
            <span class="ml-auto flex flex-wrap gap-2">
              {#if sel === 'echotest'}
                <button class="btn btn-sm {isActive ? 'btn-bad-outline' : 'btn-primary'}" disabled={busy} onclick={startStop}><Power size={13} aria-hidden="true" />{isActive ? 'Stop echo test' : 'Start echo test'}</button>
              {:else if isActive}
                <button class="btn btn-sm" disabled={busy} onclick={stop} title="Make no mode active: the radio goes idle"><Unplug size={13} aria-hidden="true" />Stop {title}</button>
              {:else}
                <button class="btn btn-sm btn-accent-outline" disabled={busy} onclick={activate} title="Make {title} the node's active mode without connecting" data-testid="make-active">Make active</button>
              {/if}
            </span>
          {/if}
        </div>

        {#if sel === 'echotest' && admin}
          <div class="space-y-1.5" data-testid="echo-codec">
            <div class="flex flex-wrap items-center gap-2">
              <label class="label !mb-0" for="echo-codec">Codec</label>
              <select id="echo-codec" class="input !min-h-9 !w-auto min-w-0 max-w-full py-1" value={codec} disabled={codecBusy} onchange={setCodec}>
                {#each ECHO_CODECS as o (o.value)}<option value={o.value}>{o.label}</option>{/each}
              </select>
            </div>
            {#if ambeNote}
              <p class="text-xs text-warn" role="status" data-testid="echo-ambe-note">The AMBE codec needs the md380-emu vocoder. Until it is installed the playback is not coded. Install it in the <a class="underline hover:no-underline" href="#/modes?mode=dmr">DMR settings</a>.</p>
            {/if}
          </div>
        {/if}

        {#if !info.configured}
          <p class="text-sm text-muted" data-testid="not-ready-line">{#if admin}This mode is not ready. Open its settings below.{:else}This mode is not set up yet. Ask an administrator.{/if}</p>
        {:else}
          <!-- Status: what it is linked to, who is talking, the AllStar counters, the echo test report. -->
          <div class="grid grid-cols-1 gap-2 sm:grid-cols-2" data-testid="mode-status">
            {#if sel !== 'echotest'}
              <div class="rounded border bg-surface-2 px-3 py-2 {linked ? 'border-ok/40' : 'border-line'}">
                <div class="text-[11px] uppercase tracking-wide text-muted">{targetLabel(info)}</div>
                {#if linked}
                  <div class="flex items-center gap-2">
                    <div class="min-w-0 flex-1 truncate font-mono font-medium text-ok" data-testid="linked-target">{targetText(st?.target)}</div>
                    {#if op}<button class="btn btn-sm btn-bad-outline shrink-0" disabled={busy} onclick={disconnect}><Unplug size={12} aria-hidden="true" />Disconnect</button>{/if}
                  </div>
                  {#if st?.links?.length && sel === 'allstar'}<div class="truncate text-xs text-muted">Links: {st.links.join(', ')}</div>{/if}
                {:else if isActive && st?.error}
                  <div class="text-sm text-danger" role="alert">{st.error}</div>
                {:else}
                  <div class="text-muted">{isActive ? 'Not connected' : 'Not the active mode'}</div>
                {/if}
                {#if info.last && !linked}<div class="mt-1 text-xs text-muted" data-testid="last-tune">Last: <button type="button" class="font-mono underline hover:no-underline" onclick={() => { text = info.last?.id ?? ''; }}>{targetText(info.last)}</button></div>{/if}
              </div>
            {/if}
            <div class="rounded border border-line bg-surface-2 px-3 py-2" aria-live="polite">
              <div class="text-[11px] uppercase tracking-wide text-muted">Now calling</div>
              <div class="flex items-center gap-1.5 text-sm">
                <Mic size={14} class="shrink-0 {talker || localTx ? 'text-danger' : 'text-muted'}" aria-hidden="true" />
                {#if localTx}<span class="min-w-0 truncate" data-testid="mode-local-tx"><b>Local station</b> · sending to {modeTitle(active, app.modes)}</span>
                {:else if talker}<span class="min-w-0 truncate" data-testid="mode-talker"><b class="font-mono">{talker.callsign || (talker.id ? `ID ${talker.id}` : 'Someone')}</b>{#if talker.name} · {talker.name}{/if}{#if talker.place} · <span class="text-muted">{talker.place}</span>{/if}</span>
                {:else}<span class="text-muted">{isActive ? 'Nobody is talking' : '—'}</span>{/if}
              </div>
            </div>
          </div>
          {#if isActive && st?.link}<p class="break-words font-mono text-xs text-muted" data-testid="link-stats" title={linkStatsTitle(st.link)}>{linkStatsText(st.link, app.node?.radio.netDropped)}</p>{/if}
          {#if isActive && st?.registration && !st.registration.registered}<p class="text-xs text-warn">Not registered yet{st.registration.lastError ? `: ${st.registration.lastError}` : ''}</p>{/if}
          {#if sel === 'echotest'}
            <p class="text-sm text-muted">{isActive ? 'Key the radio and talk, then unkey: the node plays your recording back.' : 'Start it, key the radio, talk and unkey: the node plays your recording back.'}</p>
            {#if levels}
              <p class="text-xs {levels.warnings?.length ? 'text-warn' : 'text-muted'}" data-testid="echo-report">Last test: peak {levels.peakDb.toFixed(1)} dB, level {levels.rmsDb.toFixed(1)} dB{levels.warnings?.length ? `. ${levels.warnings.join(', ')}` : ''}</p>
            {/if}
          {/if}

          <!-- The connect box. -->
          {#if op && hasTarget(info)}
            <form class="space-y-1" onsubmit={(e) => { e.preventDefault(); void connect(); }} data-testid="connect-form">
              <label class="label" for="mtarget">{targetLabel(info)}</label>
              <div class="flex flex-wrap items-stretch gap-1.5 md:flex-nowrap">
                <div class="relative min-w-0 flex-1 basis-full md:basis-0">
                  <Search size={16} class="pointer-events-none absolute left-2.5 top-1/2 z-10 -translate-y-1/2 text-muted" aria-hidden="true" />
                  {#if kind === 'nodes'}
                    <NodePicker id="mtarget" bind:value={text} placeholder={targetPlaceholder(info)} onenter={() => connect()} onpick={pickNode} inputmode="text" inputClass="!min-h-10 !pl-9" />
                  {:else if kind === 'hosts'}
                    <NodePicker id="mtarget" bind:value={text} options={hosts} placeholder={targetPlaceholder(info)} label={(o) => `${o.value} ${o.name}`.trim()} onpickoption={pickHost} onenter={() => connect()} inputmode="text" inputClass="!min-h-10 !pl-9" />
                  {:else}
                    <input id="mtarget" class="input font-mono !min-h-10 !pl-9" bind:value={text} autocomplete="off" inputmode="text" placeholder={targetPlaceholder(info)} />
                  {/if}
                </div>
                {#if sel === 'm17'}
                  <select class="input !min-h-10 !w-auto" bind:value={module} disabled={busy} aria-label="Module" title="The module (room) of the reflector" data-testid="m17-module">
                    {#each M17_MODULES as x (x)}<option value={x}>Module {x}</option>{/each}
                  </select>
                {/if}
                <button class="btn btn-primary !min-h-10" disabled={busy || !value}><Plug size={14} aria-hidden="true" />Connect</button>
                <button type="button" class="btn btn-icon !min-h-10 !w-10 !px-0 {starred ? 'border-accent' : ''}" disabled={busy || !!block} aria-pressed={!!starred} data-testid="star"
                  aria-label={starred ? 'Already a favorite' : 'Save the target as a favorite'} title={block || 'Save the target as a favorite'} onclick={openStar}>
                  <Star size={16} class={starred ? 'fill-accent text-accent' : 'text-muted'} aria-hidden="true" /></button>
              </div>
              {#if kind === 'hosts' && hostLists[sel] && !hostLists[sel].items.length}
                <p class="text-xs text-warn" role="status" data-testid="hosts-empty">{emptyListNote(sel, sel.toUpperCase())}{#if admin}{' '}<a class="underline" href="#/settings/station/hostlists">Open it</a>{/if}</p>
              {/if}
              {#if info.targetHelp}<p class="text-xs text-muted">{info.targetHelp}{#if !isActive}{' '}Connecting makes {title} the active mode.{/if}</p>{/if}
            </form>
          {/if}

          <!-- Favorites of this mode. -->
          {#if hasTarget(info)}
            <div>
              <div class="mb-1 flex items-center gap-2">
                <h3 class="text-[11px] font-semibold uppercase tracking-wide text-muted">Favorites</h3>
                {#if op}<button class="btn btn-sm ml-auto" onclick={() => { editing = null; dlg = true; }}><Plus size={13} aria-hidden="true" />Add</button>{/if}
              </div>
              {#if favs.length === 0}
                <p class="text-sm text-muted">No favorites for {title} yet.{#if op} Save a target with the star, or press Add.{/if}</p>
              {:else}
                <div class="flex flex-wrap gap-2" role="group" aria-label="Favorites for {title}" data-testid="favorites">
                  {#each favs as f, i (f.id)}
                    {@const here = favoriteActive(f, active, app.node.mode?.target)}
                    <span class="inline-flex max-w-full items-stretch" data-active={here}>
                      {#if op}
                        <button type="button" class="btn btn-pin min-w-0 border-accent/50 {here ? 'bg-accent/10' : ''} rounded-r-none" disabled={busy} title="Connect {title} to {f.target}" aria-label="Connect {title} to {f.label || f.target}" onclick={() => tap(f)}>
                          <PinFace active={here} label={f.label || f.target} sub={f.label && f.label !== f.target ? f.target : ''} dotId="fav-active" /></button>
                        <button type="button" class="btn btn-pin -ml-px rounded-none border-accent/50 !px-2" aria-pressed={f.pinned} aria-label="{f.pinned ? 'Unpin' : 'Pin'} {f.label || f.target} on the dashboard" title="Pin to the dashboard" onclick={() => pin(f)}>
                          <Pin size={14} class={f.pinned ? 'fill-accent text-accent' : 'text-muted'} aria-hidden="true" /></button>
                        <Menu label="Favorite {f.label || f.target}" btnClass="btn btn-pin -ml-px rounded-l-none border-accent/50 !px-2 text-muted" items={menu}>
                          {#snippet trigger()}<Ellipsis size={14} aria-hidden="true" />{/snippet}
                        </Menu>
                        {#snippet menu(close: () => void)}
                          <button type="button" role="menuitem" class="block w-full rounded px-2 py-1 text-left text-sm hover:bg-surface-2" onclick={() => { close(); editing = f; dlg = true; }}>Edit</button>
                          <button type="button" role="menuitem" class="block w-full rounded px-2 py-1 text-left text-sm hover:bg-surface-2 disabled:opacity-50" disabled={i === 0} onclick={() => { close(); void move(f, -1); }}>Move earlier</button>
                          <button type="button" role="menuitem" class="block w-full rounded px-2 py-1 text-left text-sm hover:bg-surface-2 disabled:opacity-50" disabled={i === favs.length - 1} onclick={() => { close(); void move(f, 1); }}>Move later</button>
                          <button type="button" role="menuitem" class="block w-full rounded px-2 py-1 text-left text-sm text-danger hover:bg-danger/10" onclick={() => { close(); delFav = f; delOpen = true; }}>Remove</button>
                        {/snippet}
                      {:else}
                        <span class="btn btn-pin cursor-default border-accent/50 hover:bg-surface" title={f.target}><PinFace active={here} label={f.label || f.target} sub={f.label && f.label !== f.target ? f.target : ''} dotId="fav-active" />{#if f.pinned}<Pin size={12} class="fill-accent text-accent" aria-label="pinned" />{/if}</span>
                      {/if}
                    </span>
                  {/each}
                </div>
              {/if}
            </div>
            {#if op && recent.length}
              <div>
                <h3 class="mb-1 text-[11px] font-semibold uppercase tracking-wide text-muted">Recent</h3>
                <div class="flex flex-wrap gap-1.5" data-testid="recent">
                  {#each recent as t (t)}<button type="button" class="btn btn-sm font-mono" onclick={() => (text = t)}>{t}</button>{/each}
                </div>
              </div>
            {/if}
          {/if}
        {/if}

        <!-- The mode's settings, inline. Administrators only; a mode that is not ready opens them. -->
        {#if admin && sel !== 'echotest'}
          <div class="rounded-md border border-line" data-testid="settings-section">
            <button type="button" class="flex min-h-10 w-full items-center gap-2 px-3 text-left text-sm font-medium" aria-expanded={settingsOpen} aria-controls="mode-settings" onclick={() => (openFor[sel] = !settingsOpen)}>
              {#if settingsOpen}<ChevronDown size={16} aria-hidden="true" />{:else}<ChevronRight size={16} aria-hidden="true" />{/if}
              <span>{title} settings</span>
              {#if !info.configured}<span class="badge border-warn text-warn">Not set up</span>{:else if vocNeeded}<span class="badge border-warn text-warn">Needs vocoder</span>{/if}
            </button>
            {#if settingsOpen}
              <div id="mode-settings" class="border-t border-line p-3">
                {#key sel}<ModeSettings {info} vocoder={voc} onvocoder={(v) => (voc = v)} />{/key}
              </div>
            {/if}
          </div>
        {/if}
      </section>
    {/if}
  {/if}
  <Footer />
</div>

<FavoriteDialog bind:open={dlg} fav={editing} initialMode={sel} initialTarget={value} />
<Confirm bind:open={delOpen} title="Remove favorite" message="Remove {delFav?.label || delFav?.target || ''} from the favorites?" confirmLabel="Remove" onconfirm={remove} />
<Dialog bind:open={starOpen} title="Save favorite">
  <form id="starform" class="space-y-2" onsubmit={saveStar}>
    <div>
      <label class="label" for="starname">Name <span class="font-normal">(optional, up to 40 characters)</span></label>
      <input id="starname" class="input" bind:value={starName} maxlength="40" autocomplete="off" placeholder={current} />
    </div>
    <p class="text-xs text-muted">{title} · <span class="font-mono">{current}</span></p>
  </form>
  {#snippet footer()}
    <button class="btn" onclick={() => (starOpen = false)}>Cancel</button>
    <button class="btn btn-primary" type="submit" form="starform" disabled={busy || !starOk}>{busy ? 'Saving…' : 'Save'}</button>
  {/snippet}
</Dialog>
