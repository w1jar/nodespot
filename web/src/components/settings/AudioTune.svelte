<script lang="ts">
  // The Audio tab: a two-step tuning wizard (receive, then transmit), the AGC both ways, and (collapsed) the static gain, the AGC's
  // maximum gain and the sound chip's counters. The tuning sliders are the sound chip's mixer levels when it has them, else the
  // static gain in station.advanced. Mixer levels apply at once; the AGC and gain are saved in station.advanced, with no restart.
  import { untrack } from 'svelte';
  import { api } from '../../lib/api';
  import { app, isAdmin, refreshNode } from '../../lib/store.svelte';
  import { router } from '../../lib/router.svelte';
  import { wantsAutosetFocus } from '../../lib/tuning';
  import { toast, toastError } from '../../lib/toast.svelte';
  import { AGC_DEFAULTS, AGC_FIELDS, AGC_TARGET_FIELDS, AUDIO_ADV_KEYS, GAIN_FIELDS, audioBody, audioDirty, audioError, fieldError, withKeys } from '../../lib/audio';
  import type { NumField } from '../../lib/audio';
  import { autosetRunning, autosetText, barPercent, dbText, fractionStep, hasSteps, isHot, levelAvailable, levelFraction, levelPercent, levelText, reportSummary, rxDetails, rxVerdict, stepFraction, stepText, targetBand } from '../../lib/levels';
  import type { AutosetStatus, InputLevel, RadioLevels, Station, StationAdvanced } from '../../lib/types';

  let lv = $state<RadioLevels | null>(null), saved = $state<Station | null>(null), adv = $state<StationAdvanced>({});
  let err = $state(''), busy = $state(false), step = $state<1 | 2>(1), toneBusy = $state(false), sw = $state(false);
  const admin = $derived(isAdmin());
  const present = $derived(app.node?.radio.present ?? false);
  const chk = 'mt-0.5 size-4 accent-[var(--accent)]';
  const dirty = $derived(!!saved && audioDirty(saved, adv, saved.id));
  const capture = $derived(!!lv && levelAvailable(lv.CaptureVolume));
  const playback = $derived(!!lv && levelAvailable(lv.PlaybackVolume));
  // The static gain fields not used as a tuning slider.
  const gainFields = $derived(GAIN_FIELDS.filter((f) => (f.key === 'rxGainDb' ? capture : playback)));
  const advFields = $derived([...gainFields, ...AGC_FIELDS.filter((f) => !AGC_TARGET_FIELDS.includes(f))]);
  const tone = $derived(!!app.node?.radio.testTone);
  const echoActive = $derived(app.node?.active === 'echotest');

  function fill(s: Station) { saved = s; adv = { ...s.advanced }; err = ''; }
  async function loadLevels() { try { lv = await api.radio(); } catch { /* the radio may be absent */ } }
  async function load() { try { fill(await api.station()); } catch (e) { toastError(e); } await loadLevels(); }
  $effect(() => { load(); const t = setInterval(loadLevels, 2000); return () => clearInterval(t); });

  // The live receive bar (only an "is it moving" indicator: the numbers flicker too fast to read) and the node's summary of the
  // last transmission, kept until the next one.
  let inp = $state<InputLevel | null>(null);
  const band = targetBand();
  const lastRx = $derived(inp?.lastRx ?? app.node?.radio.lastRx);
  const agcOn = $derived((adv.radioAgc as boolean | undefined) ?? AGC_DEFAULTS.radioAgc);
  const agcMax = $derived(Number(adv.agcMaxDb) > 0 ? Number(adv.agcMaxDb) : 18);
  const verdict = $derived(rxVerdict(lastRx, agcOn));
  async function pollInput() {
    try { inp = await api.radioInput(); } catch { /* the node may be down */ }
  }
  $effect(() => { if (!present || step !== 1) return; pollInput(); const t = setInterval(pollInput, 250); return () => clearInterval(t); });

  // The receive auto-set: the node listens to one transmission and sets the sound chip's capture step.
  let auto = $state<AutosetStatus | null>(null);
  const autoRunning = $derived(autosetRunning(auto));
  async function pollAuto() {
    try {
      // A finished run from before this page opened is not shown again.
      const was = autoRunning, st = await api.radioAutoset();
      auto = was || autosetRunning(st) ? st : null;
      if (was && !autosetRunning(st)) { await loadLevels(); await refreshNode(); }
    } catch { /* the node may be down */ }
  }
  $effect(() => { if (present) untrack(pollAuto); });
  $effect(() => { if (!autoRunning) return; const t = setInterval(pollAuto, 500); return () => clearInterval(t); });
  // Opened from the dashboard reminder ("Tune now", #/settings/station/audio?tune=1): step 1 is open, and Auto-set scrolls into view and
  // is highlighted for a few seconds once it is drawn (it needs the chip's levels, which load after the page).
  const focusAuto = wantsAutosetFocus(untrack(() => router.query));
  let autoBtn = $state<HTMLButtonElement | null>(null), emphasize = $state(false);
  $effect(() => {
    if (!focusAuto || !autoBtn) return;
    const b = autoBtn;
    b.scrollIntoView({ block: 'center', behavior: matchMedia('(prefers-reduced-motion: reduce)').matches ? 'auto' : 'smooth' });
    b.focus({ preventScroll: true });
    emphasize = true;
    const t = setTimeout(() => (emphasize = false), 4000);
    return () => clearTimeout(t);
  });
  async function startAuto() { try { auto = await api.startAutoset(); } catch (e) { toastError(e); } }
  async function cancelAuto() { try { auto = await api.cancelAutoset(); } catch (e) { toastError(e); } }

  // The sound chip's sliders sit on its hardware steps; while one is dragged its label shows the step.
  const rxSteps = $derived(lv && capture && hasSteps(lv.CaptureSteps) ? lv.CaptureSteps : 0);
  const txSteps = $derived(lv && playback && hasSteps(lv.PlaybackSteps) ? lv.PlaybackSteps : 0);
  let rxDrag = $state<number | null>(null), txDrag = $state<number | null>(null);

  async function setLevel(p: { rxVolume?: number; txVolume?: number }) { try { lv = await api.setRadio(p); await refreshNode(); } catch (e) { toastError(e); } }
  // A tuning gain slider saves that one key at once, over the station as saved now.
  async function setGain(key: 'rxGainDb' | 'txGainDb', v: number) {
    adv[key] = v || undefined;
    try {
      const fresh = await api.station();
      const body = audioBody(fresh, withKeys(fresh.advanced, { [key]: v || undefined }, [key]), fresh.id);
      await api.saveStation(body); saved = { ...fresh, advanced: body.advanced };
    } catch (e) { toastError(e); }
  }
  const gainOf = (key: 'rxGainDb' | 'txGainDb') => { const n = Number(adv[key] ?? 0); return Number.isFinite(n) ? n : 0; };
  const gainText = (n: number) => `${n > 0 ? '+' : ''}${n.toFixed(1)} dB`;

  async function sendTone() {
    toneBusy = true;
    try { await api.testTone(5); await refreshNode(); } catch (e) { toastError(e); } finally { toneBusy = false; }
  }
  async function stopTone() {
    toneBusy = true;
    try { await api.stopTestTone(); await refreshNode(); } catch (e) { toastError(e); } finally { toneBusy = false; }
  }
  async function useEcho() {
    sw = true;
    try { await api.select('echotest'); await refreshNode(); toast('Parrot is active. Key up, talk, and listen to the playback.'); } catch (e) { toastError(e); } finally { sw = false; }
  }

  async function save(ev: Event) {
    ev.preventDefault();
    if (!saved) return;
    err = audioError(adv, saved.id);
    if (err) return;
    busy = true;
    try {
      // Only this tab's keys change, over the station as saved now.
      const fresh = await api.station();
      await api.saveStation(audioBody(fresh, withKeys(fresh.advanced, adv, AUDIO_ADV_KEYS), fresh.id));
      await refreshNode(); await load(); toast('Audio settings saved');
    } catch (e) { toastError(e); } finally { busy = false; }
  }
  const verdictClass = (k: string) => (k === 'good' ? 'text-ok' : k === 'none' ? 'text-muted' : 'text-warn');
</script>

{#snippet num(f: NumField, o: Record<string, unknown>, p: string)}
  <div>
    <label class="label" for="{p}-{f.key}">{f.label} ({f.unit})</label>
    <input id="{p}-{f.key}" class="input tabular-nums" type="number" inputmode="numeric" min={f.min} max={f.max} step={f.step} bind:value={o[f.key] as number | undefined} placeholder={String(f.def)} />
    <p class="mt-1 text-xs text-muted">{f.help}</p>
  </div>
{/snippet}

{#snippet gainSlider(key: 'rxGainDb' | 'txGainDb', label: string)}
  {@const f = GAIN_FIELDS.find((x) => x.key === key)!}
  <label class="label" for="au-{key}">{label}: {gainText(gainOf(key))}</label>
  <input id="au-{key}" class="w-full accent-[var(--accent)]" type="range" min={f.min} max={f.max} step={f.step} value={gainOf(key)} disabled={!admin} onchange={(e) => setGain(key, +e.currentTarget.value)} />
{/snippet}

{#snippet saveRow()}
  {#if err}<p class="text-sm text-danger" role="alert">{err}</p>{/if}
  {#if admin}
    <div class="flex flex-wrap items-center gap-2">
      <button class="btn btn-primary" type="submit" disabled={busy || !dirty}>{busy ? 'Saving…' : 'Save'}</button>
      <button class="btn btn-accent-outline" type="button" disabled={busy || !dirty} onclick={() => saved && fill(saved)}>Revert</button>
      {#if dirty}<span class="badge" role="status">Unsaved changes</span>{/if}
    </div>
  {/if}
{/snippet}

<section class="card space-y-3 p-4" aria-label="Tuning">
  <div class="flex flex-wrap items-center justify-between gap-2">
    <h2 class="text-base font-semibold">Tuning</h2>
    <div class="flex gap-1" role="tablist" aria-label="Tuning steps">
      <button type="button" role="tab" aria-selected={step === 1} class="btn btn-sm {step === 1 ? 'btn-accent-outline' : ''}" onclick={() => (step = 1)}>1. Receive</button>
      <button type="button" role="tab" aria-selected={step === 2} class="btn btn-sm {step === 2 ? 'btn-accent-outline' : ''}" onclick={() => (step = 2)}>2. Transmit</button>
    </div>
  </div>
  {#if !present}
    <p class="text-sm text-muted">This node has no radio audio.</p>
  {:else if step === 1}
    <p class="text-sm">Key up a handheld and talk.</p>
    <div class="relative h-4 overflow-hidden rounded bg-black/10 dark:bg-white/10" role="meter" aria-label="Radio receive level" aria-valuemin={-60} aria-valuemax={0} aria-valuenow={Math.round(inp?.peakDb ?? -60)}>
      <div class="absolute inset-y-0 bg-green-500/30" style="left: {band.left}%; width: {band.width}%"></div>
      <div class="absolute inset-y-0 left-0 {isHot(inp?.peakDb ?? -90) ? 'bg-red-500' : 'bg-accent'}" style="width: {barPercent(inp?.peakDb ?? -90)}%"></div>
    </div>
    <div class="rounded border border-line p-3" data-testid="rx-card">
      <p class="text-xs text-muted">{lastRx ? `Last transmission (${(lastRx.durationMs / 1000).toFixed(1)} s)` : 'Last transmission'}</p>
      <p class="mt-1 text-sm font-semibold {verdictClass(verdict.kind)}" role="status" data-testid="tune-verdict" data-kind={verdict.kind}>{verdict.kind === 'good' ? '✓ ' : verdict.kind === 'none' ? '' : '⚠ '}{verdict.text}</p>
      {#if lastRx}
        <details class="mt-2" data-testid="rx-details">
          <summary class="cursor-pointer text-xs text-muted">Details</summary>
          <ul class="mt-1 space-y-0.5 text-xs text-muted tabular-nums">{#each rxDetails(lastRx, agcOn, agcMax) as line (line)}<li>{line}</li>{/each}</ul>
        </details>
      {/if}
    </div>
    <div>
      {#if lv && capture}
        {#if rxSteps}
          {@const cur = fractionStep(lv.CaptureVolume, rxSteps)}
          <label class="label" for="au-rx">Receive level (sound chip): {rxDrag === null ? stepText(cur, rxSteps, lv.CaptureDb) : stepText(rxDrag, rxSteps)}</label>
          <input id="au-rx" class="w-full accent-[var(--accent)]" type="range" min="0" max={rxSteps} step="1" disabled={!admin || autoRunning} value={cur}
            oninput={(e) => (rxDrag = +e.currentTarget.value)} onchange={(e) => { rxDrag = null; setLevel({ rxVolume: stepFraction(+e.currentTarget.value, rxSteps) }); }} />
        {:else}
          <label class="label" for="au-rx">Receive level (sound chip): {levelText(lv.CaptureVolume)}</label>
          <input id="au-rx" class="w-full accent-[var(--accent)]" type="range" min="0" max="100" step="1" disabled={!admin || autoRunning} value={levelPercent(lv.CaptureVolume)} onchange={(e) => setLevel({ rxVolume: levelFraction(+e.currentTarget.value) })} />
        {/if}
      {:else if saved}
        {@render gainSlider('rxGainDb', 'Receive gain')}
      {/if}
    </div>
    {#if admin && rxSteps}
      <div class="flex flex-wrap items-center gap-2">
        {#if autoRunning}
          <button type="button" class="btn btn-bad-outline" onclick={cancelAuto}>Cancel</button>
        {:else}
          <button type="button" class="btn btn-primary {emphasize ? 'ring-2 ring-accent ring-offset-2 ring-offset-bg motion-safe:animate-pulse' : ''}" data-testid="autoset-btn" data-emphasis={emphasize ? 'on' : undefined} bind:this={autoBtn} onclick={startAuto}>Auto-set</button>
        {/if}
        {#if autosetText(auto)}<span class="text-sm {auto?.state === 'failed' ? 'text-warn' : 'text-muted'}" role="status" data-testid="autoset-status">{autosetText(auto)}</span>{/if}
      </div>
    {/if}
    <div class="flex justify-end"><button type="button" class="btn btn-sm" onclick={() => (step = 2)}>Next: transmit</button></div>
  {:else}
    <div class="flex flex-wrap items-center gap-2">
      {#if tone}
        <button type="button" class="btn btn-bad-outline" disabled={toneBusy} onclick={stopTone}>Stop</button>
        <span class="text-sm text-muted" role="status">Sending a 1 kHz tone…</span>
      {:else}
        <button type="button" class="btn btn-primary" disabled={toneBusy || !admin} onclick={sendTone}>Send test tone</button>
        <span class="text-xs text-muted">1 kHz for 5 seconds.</span>
      {/if}
    </div>
    <div>
      {#if lv && playback}
        {#if txSteps}
          {@const cur = fractionStep(lv.PlaybackVolume, txSteps)}
          <label class="label" for="au-tx">Transmit level (sound chip): {txDrag === null ? stepText(cur, txSteps, lv.PlaybackDb) : stepText(txDrag, txSteps)}</label>
          <input id="au-tx" class="w-full accent-[var(--accent)]" type="range" min="0" max={txSteps} step="1" disabled={!admin} value={cur}
            oninput={(e) => (txDrag = +e.currentTarget.value)} onchange={(e) => { txDrag = null; setLevel({ txVolume: stepFraction(+e.currentTarget.value, txSteps) }); }} />
        {:else}
          <label class="label" for="au-tx">Transmit level (sound chip): {levelText(lv.PlaybackVolume)}</label>
          <input id="au-tx" class="w-full accent-[var(--accent)]" type="range" min="0" max="100" step="1" disabled={!admin} value={levelPercent(lv.PlaybackVolume)} onchange={(e) => setLevel({ txVolume: levelFraction(+e.currentTarget.value) })} />
        {/if}
      {:else if saved}
        {@render gainSlider('txGainDb', 'Transmit gain')}
      {/if}
      <p class="mt-1 text-xs text-muted">Listen on a handheld or run the parrot; set it so the tone is clear and as loud as other stations. There is no Auto-set here: the node cannot hear what it sends.</p>
    </div>
    <div class="flex flex-wrap items-center justify-between gap-2">
      <button type="button" class="btn btn-sm" onclick={() => (step = 1)}>Back: receive</button>
      {#if !echoActive}
        <button type="button" class="btn btn-sm" disabled={sw || !admin} onclick={useEcho}>Switch to parrot</button>
      {:else}<span class="text-xs text-muted">The parrot is active. Pick another mode on the Modes page when done.</span>{/if}
    </div>
    {#if app.node?.echoTest}<p class="text-xs text-muted tabular-nums" data-testid="echo-report">Last parrot: {reportSummary(app.node.echoTest)}</p>{/if}
  {/if}
</section>

{#if saved}
  <form class="space-y-4" onsubmit={save}>
    <section class="card mt-4 space-y-3 p-4" aria-label="AGC">
      <h2 class="text-base font-semibold">AGC</h2>
      <p class="text-xs text-muted">Evens out loud and quiet stations to a target speech level, each way.</p>
      <fieldset class="space-y-3" disabled={!admin}>
        {#each [{ key: 'radioAgc', label: 'Radio → network', f: AGC_TARGET_FIELDS[0]! }, { key: 'modeAgc', label: 'Network → radio', f: AGC_TARGET_FIELDS[1]! }] as row (row.key)}
          {@const on = (adv[row.key] as boolean | undefined) ?? AGC_DEFAULTS[row.key as 'radioAgc' | 'modeAgc']}
          <div class="grid items-center gap-2 sm:grid-cols-[1fr_12rem]">
            <label class="flex cursor-pointer items-start gap-2 text-sm"><input type="checkbox" class={chk} checked={on} onchange={(e) => (adv[row.key] = e.currentTarget.checked)} /><span>{row.label}</span></label>
            <div class="flex items-center gap-2">
              <label class="sr-only" for="au-{row.f.key}">{row.label} target (dBFS)</label>
              <input id="au-{row.f.key}" class="input tabular-nums" type="number" inputmode="numeric" min={row.f.min} max={row.f.max} step={row.f.step} disabled={!on} bind:value={adv[row.f.key] as number | undefined} placeholder={String(row.f.def)} aria-invalid={!!fieldError(row.f, adv[row.f.key])} />
              <span class="shrink-0 text-xs text-muted">dBFS</span>
            </div>
          </div>
        {/each}
        <p class="text-xs text-muted">Targets are -40 to -6 dBFS; empty is -22. Both are on by default.</p>
      </fieldset>
      {@render saveRow()}
    </section>

    <details class="card mt-4" data-testid="audio-advanced">
      <summary class="cursor-pointer px-4 py-3 text-sm font-semibold">Advanced</summary>
      <div class="space-y-4 border-t border-line p-4">
        <p class="text-xs text-muted">Fixed gain and AGC limits. The defaults suit most radios.</p>
        <fieldset class="grid gap-3 sm:grid-cols-2" disabled={!admin}>{#each advFields as f (f.key)}{@render num(f, adv, 'au')}{/each}</fieldset>
        {#if lv}
          <div class="space-y-1 text-xs text-muted tabular-nums" data-testid="chip-levels">
            <p>Sound chip mixer: receive {levelText(lv.CaptureVolume)}, transmit {levelText(lv.PlaybackVolume)}.</p>
            <p>Receive {dbText(lv.Rx.RMSDb)} RMS, peak {dbText(lv.Rx.PeakDb)}{#if lv.Rx.Clipped}, clipped {lv.Rx.Clipped}{/if}. Transmit {dbText(lv.Tx.RMSDb)} RMS, peak {dbText(lv.Tx.PeakDb)}{#if lv.Tx.Clipped}, clipped {lv.Tx.Clipped}{/if}.</p>
            {#if lv.DroppedRx || lv.DroppedTx || lv.Xruns}<p>Dropped frames: receive {lv.DroppedRx}, transmit {lv.DroppedTx}. Audio underruns: {lv.Xruns}.</p>{/if}
          </div>
        {/if}
        {@render saveRow()}
      </div>
    </details>
  </form>
{:else}<p class="mt-4 text-sm text-muted">Loading…</p>{/if}
