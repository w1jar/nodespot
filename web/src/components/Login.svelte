<script lang="ts">
  import { api } from '../lib/api';
  import { app, onLoggedIn } from '../lib/store.svelte';
  import { navigate } from '../lib/router.svelte';
  import Radio from '@lucide/svelte/icons/radio-tower';
  let username = $state(''), password = $state(''), busy = $state(false), err = $state('');
  async function submit(e: Event) {
    e.preventDefault(); busy = true; err = '';
    try { onLoggedIn(await api.login(username, password)); navigate('/'); }
    catch (x) { err = x instanceof Error ? x.message : String(x); }
    finally { busy = false; }
  }
</script>
<main class="mx-auto flex min-h-dvh max-w-sm flex-col justify-center px-4">
  <div class="mb-6 flex items-center gap-2"><Radio class="text-accent" size={28} /><h1 class="text-2xl font-bold tracking-tight">nodespot</h1></div>
  <form class="card space-y-4 p-5" onsubmit={submit}>
    <h2 class="text-lg font-semibold">Sign in</h2>
    <div><label class="label" for="u">Username</label><input id="u" class="input" bind:value={username} autocomplete="username" required /></div>
    <div><label class="label" for="p">Password</label><input id="p" class="input" type="password" bind:value={password} autocomplete="current-password" required /></div>
    {#if err}<p class="text-sm text-danger" role="alert">{err}</p>{/if}
    <button class="btn btn-primary w-full" disabled={busy}>{busy ? 'Signing in…' : 'Sign in'}</button>
  </form>
  {#if app.publicEnabled}<a class="mt-3 text-center text-sm text-muted underline hover:text-fg" href="#/">Back to the public status page</a>{/if}
</main>
