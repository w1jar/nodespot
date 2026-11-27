<script lang="ts">
  // The AMBE vocoder install banner: shown to admins while the install job runs (and after it failed, with a retry).
  // The page that shows it calls trackVocoder() from lib/vocoder.svelte.ts.
  import { isAdmin } from '../lib/store.svelte';
  import { toastError } from '../lib/toast.svelte';
  import { elapsedText, vocoderLastLine, vocoderStartMs, vocoderStep } from '../lib/modes';
  import { installVocoder, vocoder } from '../lib/vocoder.svelte';

  const v = $derived(vocoder.status);
  const running = $derived(v?.job === 'running');
  const failed = $derived(v?.job === 'failed' && !v.installed && !vocoder.dismissed);
  let now = $state(Date.now()), busy = $state(false);
  $effect(() => {
    if (!running) return;
    now = Date.now();
    const t = setInterval(() => { now = Date.now(); }, 1000);
    return () => clearInterval(t);
  });
  async function retry() {
    if (busy) return;
    busy = true;
    try { await installVocoder(); } catch (e) { toastError(e); } finally { busy = false; }
  }
</script>

{#if isAdmin() && running}
  <div class="space-y-1 rounded border border-warn/60 bg-warn/5 px-3 py-2 text-sm" role="status" data-testid="vocoder-banner">
    <p>Installing the AMBE vocoder. This can take a few minutes; BrandMeister, TGIF, DMR, YSF and NXDN have no voice until it finishes.</p>
    <p class="text-xs text-muted" data-testid="vocoder-progress">{elapsedText(vocoderStartMs(v, vocoder.firstSeen), now)} · {vocoderStep(v?.jobLog)}</p>
  </div>
{:else if isAdmin() && failed}
  <div class="flex flex-wrap items-center gap-2 rounded border border-danger/60 bg-danger/5 px-3 py-2 text-sm" role="status" data-testid="vocoder-failed">
    <span class="min-w-0 flex-1 break-words text-danger">The AMBE vocoder install failed{vocoderLastLine(v?.jobLog) ? `: ${vocoderLastLine(v?.jobLog)}` : ''}</span>
    {#if v?.supported !== false}<button type="button" class="btn btn-primary btn-sm" disabled={busy} onclick={retry}>Install</button>{/if}
    <button type="button" class="btn btn-sm" onclick={() => { vocoder.dismissed = true; }}>Dismiss</button>
  </div>
{/if}
