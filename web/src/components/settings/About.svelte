<script lang="ts">
  import { api } from '../../lib/api';
  import { app } from '../../lib/store.svelte';
  import { toastError } from '../../lib/toast.svelte';
  import type { About } from '../../lib/types';
  import { bugReportUrl, featureRequestUrl, releasesUrl } from '../../lib/util';

  let about = $state<About | null>(null);
  async function loadAbout() { try { about = await api.about(); } catch (e) { toastError(e); } }
  const platformText = $derived(about ? [about.platform.model, `${about.platform.os}/${about.platform.arch}`].filter(Boolean).join(', ') : '');
  const reportUrl = $derived(bugReportUrl({ version: about?.version || app.version, hardware: about?.platform.hardware, os: about?.platform.distro }));
  $effect(() => { loadAbout(); });
</script>

<section class="card space-y-3 p-4" aria-label="About">
  <h2 class="text-base font-semibold">About</h2>
  <dl class="grid grid-cols-[auto_1fr] gap-x-4 gap-y-1 text-sm">
    <dt class="text-muted">nodespot</dt><dd class="font-mono">{about?.version || app.version || '…'}</dd>
    <dt class="text-muted">Platform</dt><dd class="break-words">{about ? platformText : '…'}</dd>
    <dt class="text-muted">OS</dt><dd class="break-words">{about ? about.platform.distro || 'n/a' : '…'}</dd>
  </dl>
  <div class="flex flex-wrap gap-2">
    <a class="btn" href={reportUrl} target="_blank" rel="noopener noreferrer">Report a bug</a>
    <a class="btn" href={featureRequestUrl} target="_blank" rel="noopener noreferrer">Request a feature</a>
    <a class="btn" href={releasesUrl} target="_blank" rel="noopener noreferrer">Release notes</a>
  </div>
</section>
