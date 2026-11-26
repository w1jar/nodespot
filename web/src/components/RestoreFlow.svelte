<script lang="ts">
  import { api, ApiError, MAX_BACKUP_BYTES } from '../lib/api';
  import type { RestorePreview, RestoreResult, RestoreStep } from '../lib/types';
  import Confirm from './Confirm.svelte';
  import Check from '@lucide/svelte/icons/check';
  import X from '@lucide/svelte/icons/x';
  import Refresh from '@lucide/svelte/icons/refresh-cw';
  import Warn from '@lucide/svelte/icons/triangle-alert';
  import Upload from '@lucide/svelte/icons/upload';

  /** `admin`: a logged-in admin re-enters the password. `setup`: first-run page, authorised by `code`. */
  let { mode, code = '', oncancel }: { mode: 'admin' | 'setup'; code?: string; oncancel?: () => void } = $props();

  type Stage = 'pick' | 'uploading' | 'preview' | 'applying' | 'waiting' | 'done';
  let stage = $state<Stage>('pick');
  let file = $state<File | null>(null), password = $state(''), err = $state('');
  let progress = $state(0), id = $state(''), preview = $state<RestorePreview | null>(null);
  let confirmOpen = $state(false), steps = $state<RestoreStep[]>([]), result = $state<RestoreResult | null>(null);
  let waitNote = $state('');

  const mb = (n: number) => (n / 1048576).toFixed(1);

  function pick(e: Event) {
    err = ''; const f = (e.currentTarget as HTMLInputElement).files?.[0] ?? null;
    if (f && f.size > MAX_BACKUP_BYTES) { err = `That file is ${mb(f.size)} MiB; the limit is 256 MiB.`; file = null; return; }
    file = f;
  }
  const UPLOAD_ERR: Record<string, string> = {
    bad_backup: 'This is not a valid nodespot backup (or it is damaged).',
    backup_too_new: 'This backup was made by a newer nodespot. Update nodespot first, then try again.',
    too_large: 'The file is too large (limit 256 MiB).',
    bad_password: 'Wrong password.',
    bad_setup_code: 'Wrong setup code.',
    helper_unavailable: 'The privileged helper is not available.',
    busy: 'Another backup or restore is running.',
  };
  /** Codes whose friendly sentence is followed by the server's own reason. */
  const WITH_DETAIL = new Set(['bad_backup', 'helper_unavailable', 'busy']);
  function uploadError(x: unknown): string {
    if (!(x instanceof ApiError)) return x instanceof Error ? x.message : String(x);
    const base = UPLOAD_ERR[x.code];
    if (!base) return x.message;
    return WITH_DETAIL.has(x.code) && x.message ? `${base} Details: ${x.message}` : base;
  }
  async function upload(e: Event) {
    e.preventDefault(); err = '';
    if (!file) { err = 'Choose a backup file.'; return; }
    if (mode === 'admin' && !password) { err = 'Enter your password.'; return; }
    if (mode === 'setup' && !code.trim()) { err = 'Enter the setup code first.'; return; }
    stage = 'uploading'; progress = 0;
    try {
      const r = await api.restoreUpload(file, mode === 'admin' ? { password } : { code: code.trim() }, (f) => (progress = f));
      id = r.id; preview = r.preview; password = ''; stage = 'preview';
    } catch (x) {
      stage = 'pick';
      err = uploadError(x);
    }
  }
  async function cancel() {
    if (id) { try { await api.restoreCancel(id); } catch { /* already gone */ } }
    id = ''; preview = null; oncancel?.();
  }

  async function apply() {
    stage = 'applying'; err = '';
    try {
      const r = await api.restoreApply(id, mode === 'setup' ? code.trim() : undefined);
      steps = r.steps;
      if (r.restarting) { stage = 'waiting'; void waitForServer(); }
      else { await loadResult(); stage = 'done'; void pollHealth(); }
    } catch (x) {
      err = x instanceof Error ? x.message : String(x); stage = 'preview';
    }
  }
  const sleep = (ms: number) => new Promise((r) => setTimeout(r, ms));
  /** The server restarts: poll until it answers again (connection errors are expected). */
  async function waitForServer() {
    const start = Date.now(); let sawDown = false;
    waitNote = 'Waiting for nodespot to restart…';
    while (Date.now() - start < 4 * 60_000) {
      await sleep(2000);
      try {
        await api.setupNeeded();
        if (sawDown || Date.now() - start > 12_000) break;
      } catch { sawDown = true; waitNote = 'nodespot is restarting…'; }
    }
    await loadResult(); stage = 'done'; void pollHealth();
  }
  /** The server runs its post-restart health checks for up to three minutes (`health.pending`); keep reading until they are final. */
  async function pollHealth() {
    for (let i = 0; i < 90 && result?.health.pending; i++) {
      await sleep(2500);
      try { result = await api.restoreResult(); } catch { /* keep the last one */ }
    }
  }
  async function loadResult() {
    for (let i = 0; i < 5 && !result; i++) {
      try { result = await api.restoreResult(); } catch (x) { if (x instanceof ApiError && x.status !== 0 && x.status < 500) break; await sleep(2000); }
    }
  }
  const finishSteps = $derived(result?.steps ?? steps);
  const checklist = $derived(result?.checklist ?? preview?.checklist ?? []);
  function login() { location.hash = '#/login'; location.reload(); }

  const when = (s: string) => { const d = new Date(s); return isNaN(d.getTime()) ? s : d.toLocaleString(); };
</script>

<div class="space-y-4 text-sm">
  {#if stage === 'pick' || stage === 'uploading'}
    <form class="space-y-3" onsubmit={upload}>
      <p class="text-muted">Upload a backup file made by nodespot. You will see exactly what will change before anything is touched.</p>
      <div>
        <label class="label" for="rf-file">Backup file (.tar.gz, up to 256 MiB)</label>
        <input id="rf-file" class="input" type="file" accept=".gz,.tgz,application/gzip" disabled={stage === 'uploading'} onchange={pick} />
        {#if file}<p class="mt-1 text-xs text-muted">{file.name}, {mb(file.size)} MiB</p>{/if}
      </div>
      {#if mode === 'admin'}
        <div>
          <label class="label" for="rf-pw">Your password</label>
          <input id="rf-pw" class="input" type="password" autocomplete="current-password" bind:value={password} disabled={stage === 'uploading'} />
          <p class="mt-1 text-xs text-muted">Re-enter your password to confirm.</p>
        </div>
      {/if}
      {#if stage === 'uploading'}
        <div role="progressbar" aria-valuemin="0" aria-valuemax="100" aria-valuenow={Math.round(progress * 100)} class="h-2 overflow-hidden rounded bg-surface-2"><div class="h-full bg-accent transition-[width]" style="width:{progress * 100}%"></div></div>
        <p class="text-xs text-muted" role="status">{progress < 1 ? `Uploading… ${Math.round(progress * 100)}%` : 'Checking the backup…'}</p>
      {/if}
      {#if err}<p class="text-danger" role="alert">{err}</p>{/if}
      <div class="flex flex-wrap gap-2">
        <button class="btn btn-primary" disabled={stage === 'uploading' || !file}><Upload size={14} />Upload and check</button>
        {#if oncancel}<button type="button" class="btn" disabled={stage === 'uploading'} onclick={() => oncancel?.()}>Cancel</button>{/if}
      </div>
    </form>
  {:else if preview && (stage === 'preview' || stage === 'applying')}
    {@const b = preview.backup}
    <section aria-label="Backup summary">
      <h3 class="text-base font-semibold">Backup of {b.hostname}</h3>
      <dl class="mt-2 grid grid-cols-[auto_1fr] gap-x-4 gap-y-1">
        {#if b.callsign}<dt class="text-muted">Callsign</dt><dd class="font-mono">{b.callsign}</dd>{/if}
        <dt class="text-muted">Created</dt><dd>{when(b.created)}{#if b.createdBy}{" "}by {b.createdBy}{/if}</dd>
        <dt class="text-muted">nodespot</dt><dd class="font-mono">{b.nodespotVersion}</dd>
        {#if b.osName || b.osCodename}<dt class="text-muted">System</dt><dd class="break-words">{b.osName || b.osCodename}{#if preview.node.osCodename && b.osCodename && preview.node.osCodename !== b.osCodename}{" "}<span class="text-warn">(this node: {preview.node.osName || preview.node.osCodename})</span>{/if}</dd>{/if}
        <dt class="text-muted">Architecture</dt><dd class="font-mono">{b.arch}</dd>
        {#if b.channel}<dt class="text-muted">Update channel</dt><dd>{b.channel}</dd>{/if}
      </dl>
    </section>

    {#if preview.warnings.length}
      <section class="space-y-1 rounded border border-warn bg-warn/10 p-3" aria-label="Warnings" role="alert">
        <h3 class="flex items-center gap-1.5 font-semibold text-warn"><Warn size={16} />Please read</h3>
        <ul class="list-disc space-y-0.5 pl-5">{#each preview.warnings as w}<li>{w.message}</li>{/each}</ul>
      </section>
    {/if}

    <section aria-label="Node data">
      <h3 class="font-semibold">Node</h3>
      {#if preview.modes.length}<p class="mt-1">Modes set up: <span class="font-mono">{preview.modes.join(', ')}</span>.</p>{:else}<p class="mt-1 text-muted">No modes are set up in this backup.</p>{/if}
      {#if preview.missingSecrets.length}<p class="mt-1 text-warn">No password in the backup for: <span class="font-mono">{preview.missingSecrets.join(', ')}</span>. Set them up again after the restore.</p>{/if}
    </section>

    <section aria-label="nodespot data">
      <h3 class="font-semibold">nodespot</h3>
      <p class="mt-1">These users will exist afterwards. Log in with your old password.</p>
      <ul class="mt-1 flex flex-wrap gap-1.5">{#each preview.users as u}<li class="badge">{u.username} · {u.role}</li>{/each}</ul>
      <p class="mt-1 text-muted">{preview.favorites} favorite{preview.favorites === 1 ? '' : 's'}, {preview.schedules} schedule{preview.schedules === 1 ? '' : 's'}, settings and audit log.
        {#if preview.parts.secrets}Mode passwords included.{:else} Mode passwords not included.{/if}</p>
    </section>

    {#if preview.radio}
      {@const r = preview.radio}
      <section class="rounded border border-line p-3" aria-label="Radio settings">
        <h3 class="font-semibold">Radio module settings (SA818)</h3>
        <dl class="mt-1 grid grid-cols-[auto_1fr] gap-x-4 gap-y-0.5">
          <dt class="text-muted">Bandwidth</dt><dd>{r.bandwidth}</dd>
          <dt class="text-muted">RX / TX</dt><dd class="font-mono">{r.rxMHz} / {r.txMHz} MHz</dd>
          <dt class="text-muted">Tone</dt><dd class="break-words">{r.rxTone || 'none'} / {r.txTone || 'none'}</dd>
          <dt class="text-muted">Squelch</dt><dd>{r.squelch}</dd>
        </dl>
        <p class="mt-1 text-xs text-muted">The module keeps its own settings. They are written to the node, then programmed the next time you save the radio.</p>
      </section>
    {/if}

    {#if preview.checklist.length}
      <section aria-label="Set up separately">
        <h3 class="font-semibold">Set up separately</h3>
        <p class="text-xs text-muted">A backup does not include these. Set them up on this node yourself.</p>
        <dl class="mt-1 grid grid-cols-[auto_1fr] gap-x-4 gap-y-0.5">{#each preview.checklist as c}<dt class="text-muted">{c.item}</dt><dd class="break-words">{c.value}</dd>{/each}</dl>
      </section>
    {/if}

    {#if err}<p class="text-danger" role="alert">{err}</p>{/if}
    <div class="flex flex-wrap items-center gap-2 border-t border-line pt-3">
      <button class="btn btn-primary" disabled={stage === 'applying'} onclick={() => (confirmOpen = true)}>{#if stage === 'applying'}<Refresh size={14} class="animate-spin" />Restoring…{:else}Restore{/if}</button>
      <button class="btn" disabled={stage === 'applying'} onclick={cancel}>Cancel</button>
    </div>
  {:else if stage === 'waiting'}
    <section class="space-y-3" aria-label="Restarting">
      <p class="flex items-center gap-2 font-medium" role="status"><Refresh size={14} class="animate-spin" />{waitNote}</p>
      <p class="text-muted">The node and nodespot are restarting. This takes a minute or two. Keep this page open.</p>
      {@render stepList(steps)}
    </section>
  {:else if stage === 'done'}
    <section class="space-y-4" aria-label="Restore result">
      <h3 class="text-base font-semibold">{result ? (result.health.pending ? 'Restore finished, checking health' : result.health.ok ? 'Restore finished' : 'Restore finished, with problems') : 'Restore finished'}</h3>
      {#if result}<p class="text-xs text-muted">Backup from {when(result.backupCreated)}, restored {when(result.at)}.</p>
      {:else}<p class="text-muted">The server is back, but the result could not be read yet. Log in to continue.</p>{/if}
      {@render stepList(finishSteps)}
      {#if result}
        <div>
          <h4 class="label">Health check</h4>
          {#if result.health.pending}
            <p class="mb-1 flex items-center gap-2 font-medium" role="status"><Refresh size={14} class="animate-spin" />Running health checks…</p>
          {:else}
            <p class="mb-1 font-medium {result.health.ok ? 'text-ok' : 'text-danger'}" role="status">{result.health.ok ? 'Healthy' : 'Health check failed'}</p>
          {/if}
          <ul class="space-y-0.5">
            {#each result.health.checks as c}
              <li class="flex items-start gap-1.5">
                {#if c.ok}<Check size={14} class="mt-0.5 shrink-0 text-ok" aria-label="OK" />{:else}<X size={14} class="mt-0.5 shrink-0 text-danger" aria-label="Failed" />{/if}
                <span><span class="font-medium">{c.name}</span>{#if c.detail}<span class="text-muted">&nbsp;· {c.detail}</span>{/if}</span>
              </li>
            {/each}
          </ul>
        </div>
      {/if}
      {#if checklist.length}
        <div>
          <h4 class="label">Still to do on this node</h4>
          <dl class="grid grid-cols-[auto_1fr] gap-x-4 gap-y-0.5">{#each checklist as c}<dt class="text-muted">{c.item}</dt><dd class="break-words">{c.value}</dd>{/each}</dl>
        </div>
      {/if}
      <p class="text-muted">Sessions are not restored. Log in with your old account and password.</p>
      <button class="btn btn-primary" onclick={login}>Log in</button>
    </section>
  {/if}
</div>

{#snippet stepList(list: RestoreStep[])}
  {#if list.length}
    <ul class="space-y-0.5">
      {#each list as s}
        <li class="flex items-start gap-1.5">
          {#if s.ok}<Check size={14} class="mt-0.5 shrink-0 text-ok" aria-label="OK" />{:else}<X size={14} class="mt-0.5 shrink-0 text-danger" aria-label="Failed" />{/if}
          <span><span class="font-medium">{s.name}</span>{#if s.detail}<span class="text-muted">&nbsp;· {s.detail}</span>{/if}</span>
        </li>
      {/each}
    </ul>
  {/if}
{/snippet}

<Confirm bind:open={confirmOpen} title="Restore this backup?" confirmLabel="Restore and restart"
  message="The active mode will disconnect and the node will restart. Do not run the old node at the same time: both would connect with the same credentials."
  onconfirm={apply} />
