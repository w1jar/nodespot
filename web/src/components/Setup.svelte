<script lang="ts">
  import { api, ApiError } from '../lib/api';
  import { app, onLoggedIn } from '../lib/store.svelte';
  import { navigate, router } from '../lib/router.svelte';
  import RestoreFlow from './RestoreFlow.svelte';
  import Radio from '@lucide/svelte/icons/radio-tower';
  // A setup link looks like #/setup?code=XXXX; prefill (and hide) the code.
  const linkCode = new URLSearchParams(router.query).get('code')?.trim() ?? '';
  let code = $state(linkCode), showCode = $state(!linkCode), username = $state('admin'), password = $state(''), confirm = $state(''), busy = $state(false), err = $state(''), choice = $state<'create' | 'restore'>('create');
  async function submit(e: Event) {
    e.preventDefault(); err = '';
    if (password.length < 8) { err = 'Password must be at least 8 characters.'; return; }
    if (password !== confirm) { err = 'Passwords do not match.'; return; }
    busy = true;
    try {
      await api.setup(code.trim(), username.trim(), password);
      app.setupNeeded = false;
      onLoggedIn(await api.session()); navigate('/');
    } catch (x) {
      if (x instanceof ApiError && x.code === 'setup_done') { app.setupNeeded = false; navigate('/login'); return; }
      if (x instanceof ApiError && x.code === 'bad_setup_code') showCode = true;
      err = x instanceof Error ? x.message : String(x);
    }
    finally { busy = false; }
  }
</script>
<main class="mx-auto flex min-h-dvh {choice === 'restore' ? 'max-w-2xl' : 'max-w-sm'} flex-col justify-center px-4 py-8">
  <div class="mb-6 flex items-center gap-2"><Radio class="text-accent" size={28} /><h1 class="text-2xl font-bold tracking-tight">nodespot setup</h1></div>
  <div class="mb-3 grid grid-cols-2 gap-1 rounded-md border border-line bg-surface-2/40 p-1" role="tablist" aria-label="Setup choice">
    <button type="button" role="tab" aria-selected={choice === 'create'} class="btn {choice === 'create' ? 'btn-primary' : 'border-transparent bg-transparent'}" onclick={() => (choice = 'create')}>Create admin</button>
    <button type="button" role="tab" aria-selected={choice === 'restore'} class="btn {choice === 'restore' ? 'btn-primary' : 'border-transparent bg-transparent'}" onclick={() => (choice = 'restore')}>Restore from a backup</button>
  </div>
  {#if choice === 'restore'}
  <div class="card space-y-4 p-5">
    <p class="text-sm text-muted">Setting up a replacement for an existing node? Restore its backup instead of creating a new administrator. The setup code is required (see below).</p>
    <div><label class="label" for="rc">Setup code</label><input id="rc" class="input font-mono" bind:value={code} autocomplete="off" required /></div>
    <p class="text-xs text-muted">Show it on the server with <code class="font-mono">sudo nodespot admin setup-code</code> (also in <code class="font-mono">/var/lib/nodespot/setup-code</code>), or use the setup link the installer printed.</p>
    <RestoreFlow mode="setup" {code} />
  </div>
  {:else}
  <form class="card space-y-4 p-5" onsubmit={submit}>
    <p class="text-sm text-muted">Create the first administrator.{#if showCode} Use the setup link the installer printed, or enter the code shown by <code class="font-mono">sudo nodespot admin setup-code</code>. You can also run <code class="font-mono">sudo nodespot admin create</code> on the server.{/if}</p>
    {#if showCode}
      <div><label class="label" for="c">Setup code</label><input id="c" class="input font-mono" bind:value={code} autocomplete="off" required /></div>
    {:else}
      <p class="text-sm text-muted">Setup code filled in from the link. <button type="button" class="underline" onclick={() => (showCode = true)}>change</button></p>
    {/if}
    <div><label class="label" for="u">Admin username</label><input id="u" class="input" bind:value={username} autocomplete="username" required /></div>
    <div><label class="label" for="p">Password</label><input id="p" class="input" type="password" bind:value={password} autocomplete="new-password" required /></div>
    <div><label class="label" for="p2">Confirm password</label><input id="p2" class="input" type="password" bind:value={confirm} autocomplete="new-password" required /></div>
    {#if err}<p class="text-sm text-danger" role="alert">{err}</p>{/if}
    <button class="btn btn-primary w-full" disabled={busy}>{busy ? 'Creating…' : 'Create admin and sign in'}</button>
  </form>
  {/if}
</main>
