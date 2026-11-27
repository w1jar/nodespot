<script lang="ts">
  // Overview: the station (radio, PTT/COS, levels, ID), the active mode and target, who is talking,
  // pinned favorites, schedules and one Last heard list across all modes.
  import { api } from '../lib/api';
  import { app, canOperate, clock, isAdmin, refreshNode } from '../lib/store.svelte';
  import { TUNE_HREF, showTuneBanner } from '../lib/tuning';
  import { toast, toastError } from '../lib/toast.svelte';
  import { favoriteActive, modeTitle, needsVocoder, pinnedFavorites, stateText, targetBody, targetText } from '../lib/modes';
  import { agoText, who } from '../lib/nowcalling';
  import { mergeHeard } from '../lib/lastheard';
  import type { Favorite, RadioLevels, Reading } from '../lib/types';
  import VocoderBanner from './VocoderBanner.svelte';
  import { trackVocoder, vocoder } from '../lib/vocoder.svelte';
  import LastHeardTable from './LastHeardTable.svelte';
  import SchedulesCard from './SchedulesCard.svelte';
  import Footer from './Footer.svelte';
  import RadioTower from '@lucide/svelte/icons/radio-tower';
  import Mic from '@lucide/svelte/icons/mic';
  import Unplug from '@lucide/svelte/icons/unplug';
  import PinFace from './PinFace.svelte';
  import SlidersHorizontal from '@lucide/svelte/icons/sliders-horizontal';
  import TriangleAlert from '@lucide/svelte/icons/triangle-alert';

  const op = $derived(canOperate());
  const node = $derived(app.node);
  const feed = $derived(mergeHeard(app.nowcalling?.recent, app.heard));
  const pins = $derived(pinnedFavorites(app.favorites));

  // The sound chip's levels, polled while the page is open (admin only: the endpoint is not open to viewers).
  let lv = $state<RadioLevels | null>(null);
  $effect(() => {
    if (app.user?.role !== 'admin' || !node?.radio.present) return;
    let stop = false;
    const tick = () => api.radio().then((l) => { if (!stop) lv = l; }).catch(() => {});
    tick();
    const t = setInterval(tick, 2000);
    return () => { stop = true; clearInterval(t); };
  });
  $effect(() => { if (isAdmin()) return trackVocoder(); });
  const db = (r: Reading | undefined) => (r && Number.isFinite(r.RMSDb) && r.RMSDb > -120 ? `${r.RMSDb.toFixed(0)} dB` : '—');

  const alerts = $derived.by(() => {
    const a: { key: string; text: string; href: string }[] = [];
    if (node && !node.running) a.push({ key: 'node', text: `The node is not running${node.error ? `: ${node.error}` : ''}`, href: '/settings/system/node' });
    if (node?.mode?.state === 'error') a.push({ key: 'mode', text: `${modeTitle(node.active ?? '', app.modes)}: ${node.mode.error || node.mode.detail || 'error'}`, href: '/modes' });
    if (node?.registration && !node.registration.registered && node.active === 'allstar') a.push({ key: 'reg', text: `AllStarLink registration: ${node.registration.lastError || 'not registered yet'}`, href: '/modes?mode=allstar' });
    return a;
  });

  const tuneBanner = $derived(showTuneBanner(app.user?.role, node));
  async function dismissTune() {
    if (busy) return;
    busy = 'tune';
    try { await api.dismissTuning(); await refreshNode(); } catch (e) { toastError(e); } finally { busy = ''; }
  }

  let busy = $state('');
  async function unlink() {
    if (busy || !op) return;
    busy = 'unlink';
    try { await api.disconnect(); toast('Disconnected'); } catch (e) { toastError(e); } finally { busy = ''; }
  }
  async function tap(f: Favorite) {
    if (busy || !op) return;
    busy = String(f.id);
    try { await api.select(f.mode, targetBody(f.target, f.label && f.label !== f.target ? { name: f.label } : {})); toast(`${modeTitle(f.mode, app.modes)}: connecting to ${f.label || f.target}`);
      if (isAdmin() && needsVocoder(app.modes.find((m) => m.name === f.mode), vocoder.status)) toast('Connected, but no voice until the AMBE vocoder is installed.', 'info', 7000); } catch (e) { toastError(e); } finally { busy = ''; }
  }
  const talker = $derived(node?.mode?.talker);
  /** A local station is transmitting into the receiver and its audio goes to the active mode. */
  const localTx = $derived(!!node?.radio.rxActive && !!node?.active);
  const idText = $derived.by(() => {
    const i = node?.id;
    if (!i) return '';
    if (!i.enabled) return 'Off';
    if (i.owed) return `Due${i.why ? ` (${i.why})` : ''}`;
    return i.last ? `Last ${agoText((clock.now - Date.parse(i.last)) / 1000)}` : 'On';
  });
</script>

<div class="space-y-3">
  {#if !node}
    <p class="py-10 text-center text-sm text-muted" role="status">Loading node status…</p>
  {:else}
    <SchedulesCard />
    {#if tuneBanner}
      <section class="flex flex-wrap items-center gap-x-3 gap-y-2 rounded-md border border-warn/50 bg-warn/10 px-3 py-2 text-sm" aria-label="Tune your audio" data-testid="tune-banner">
        <SlidersHorizontal size={15} class="shrink-0 text-warn" aria-hidden="true" />
        <p class="min-w-0 flex-1 basis-48">Your radio audio hasn't been tuned yet.</p>
        <div class="flex shrink-0 items-center gap-2">
          <a class="btn btn-primary btn-sm" href={TUNE_HREF} data-testid="tune-now">Tune now</a>
          <button type="button" class="btn btn-sm" disabled={!!busy} data-testid="tune-dismiss" onclick={dismissTune}>Dismiss</button>
        </div>
      </section>
    {/if}
    {#if alerts.length}
      <section class="space-y-1" aria-label="Alerts" data-testid="alerts">
        {#each alerts as a (a.key)}
          <a href={'#' + a.href} class="flex items-center gap-2 rounded-md border border-warn/50 bg-warn/10 px-3 py-1.5 text-sm text-warn hover:bg-warn/20">
            <TriangleAlert size={15} class="shrink-0" aria-hidden="true" /><span class="min-w-0">{a.text}</span>
          </a>
        {/each}
      </section>
    {/if}

    <section class="grid grid-cols-1 gap-2 md:grid-cols-2" aria-label="Status">
      <div class="card space-y-2 px-3 py-2" data-testid="station-card">
        <div class="flex items-baseline gap-2">
          <span class="inline-flex items-center gap-1 text-base font-semibold"><RadioTower size={14} class="text-accent" aria-hidden="true" />{node.callsign || 'Station'}</span>
          <span class="text-xs text-muted">{node.radio.present ? 'Radio' : 'No radio'}</span>
        </div>
        <div class="flex flex-wrap items-center gap-1.5 text-xs">
          <span class="badge {node.radio.ptt ? 'border-danger text-danger' : ''}" data-testid="ptt">PTT {node.radio.ptt ? 'on' : 'off'}</span>
          <span class="badge {node.radio.cos ? 'border-ok text-ok' : ''}" data-testid="cos">COS {node.radio.cos ? 'active' : 'idle'}</span>
          <span class="badge" data-testid="id-state" title="Station ID. The licensee is responsible for IDing.">ID {idText}</span>
        </div>
        {#if lv}
          <dl class="grid grid-cols-[auto_1fr_auto_1fr] gap-x-3 gap-y-0.5 text-xs" data-testid="levels">
            <dt class="text-muted">RX level</dt><dd class="tabular-nums">{db(lv.Rx)}</dd>
            <dt class="text-muted">TX level</dt><dd class="tabular-nums">{db(lv.Tx)}</dd>
          </dl>
        {/if}
      </div>

      <VocoderBanner />
      <div class="card space-y-1 px-3 py-2" data-testid="mode-card">
        <div class="flex items-baseline gap-2">
          <span class="text-xs font-semibold uppercase tracking-wide text-muted">Mode</span>
          <a class="min-w-0 flex-1 truncate text-base font-semibold hover:underline" href="#/modes">{node.active ? modeTitle(node.active, app.modes) : 'None active'}</a>
          {#if node.active}<span class="badge {node.mode?.state === 'connected' ? 'text-ok' : node.mode?.state === 'error' ? 'text-danger' : 'text-warn'}">{stateText(node.mode?.state)}</span>{/if}
        </div>
        <div class="flex items-center gap-2">
          <div class="min-w-0 flex-1 truncate text-sm {node.mode?.target ? 'font-mono' : 'text-muted'}" data-testid="mode-target">{node.mode?.target ? targetText(node.mode.target) : node.active ? 'No target' : 'Pick a mode on the Modes page.'}</div>
          {#if op && node.mode?.target && (node.mode.state === 'connected' || node.mode.state === 'connecting')}
            <button type="button" class="btn btn-sm btn-bad-outline shrink-0" disabled={!!busy} data-testid="tile-unlink" onclick={unlink}><Unplug size={12} aria-hidden="true" />Disconnect</button>
          {/if}
        </div>
        <div class="-mx-2 flex items-center gap-1.5 rounded border border-transparent px-2 py-1 text-sm {localTx && node.active ? 'talk-live talk-local' : talker || app.nowcalling?.current ? 'talk-live' : ''}" data-testid="talker" data-live={localTx && node.active ? 'local' : talker || app.nowcalling?.current ? 'remote' : undefined} aria-live="polite">
          <Mic size={14} class="shrink-0 {localTx && node.active ? 'talk-mic text-accent' : talker || app.nowcalling?.current ? 'talk-mic text-danger' : 'text-muted'}" aria-hidden="true" />
          {#if localTx && node.active}<span class="min-w-0 truncate" data-testid="local-tx"><b>Local station</b> · sending to {modeTitle(node.active, app.modes)}</span>
          {:else if talker}<span class="min-w-0 truncate"><b class="font-mono">{talker.callsign || (talker.id ? `ID ${talker.id}` : 'Someone')}</b>{#if talker.name}{' · '}{talker.name}{/if}{#if talker.place}{' · '}<span class="text-muted">{talker.place}</span>{/if}</span>
          {:else if app.nowcalling?.current}<span class="min-w-0 truncate"><b class="font-mono">{who(app.nowcalling.current)}</b></span>
          {:else}<span class="text-muted">Nobody is talking</span>{/if}
        </div>
      </div>
    </section>

    {#if pins.length}
      <section aria-label="Pinned favorites">
        <h2 class="mb-1 text-sm font-semibold uppercase tracking-wide text-muted">Pinned</h2>
        <div class="flex flex-wrap gap-2" data-testid="pins">
          {#each pins as p (p.id)}
            {@const here = favoriteActive(p, node.active, node.mode?.target)}
            <button type="button" class="btn btn-pin max-w-full {here ? 'btn-accent-outline' : ''}" disabled={!op || !!busy} aria-pressed={here} aria-label="Connect {modeTitle(p.mode, app.modes)} to {p.label || p.target}" title="Switch to {modeTitle(p.mode, app.modes)} and connect to {p.target}" data-pin={p.id} onclick={() => tap(p)}>
              <PinFace active={here} dotId="pin-active" tag={modeTitle(p.mode, app.modes)} label={p.label || p.target} sub={p.label && p.label !== p.target ? p.target : ''} />
            </button>
          {/each}
        </div>
      </section>
    {/if}

    <section aria-label="Last heard">
      <h2 class="mb-1 text-sm font-semibold uppercase tracking-wide text-muted">Last heard</h2>
      <div class="rounded-md border border-line bg-surface px-3 py-1">
        {#if feed.length === 0}
          <p class="py-3 text-center text-sm text-muted">Nothing heard yet.</p>
        {:else}
          <LastHeardTable rows={feed} testid="activity" />
        {/if}
      </div>
    </section>
  {/if}
  <Footer />
</div>
