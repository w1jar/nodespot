<script lang="ts">
  import '@xterm/xterm/css/xterm.css';
  import { onDestroy } from 'svelte';
  import { api } from '../../lib/api';
  import { toast, toastError } from '../../lib/toast.svelte';
  import { wsURL, resizeMsg, endMsg, endText, mockSession, newSessionId, resumeAction, SESSION_KEY } from '../../lib/termconn';
  import type { TerminalStatus, TerminalSessionInfo } from '../../lib/types';
  import Toggle from '../Toggle.svelte';

  let st = $state<TerminalStatus | null>(null), err = $state(''), busy = $state(false);
  let phase = $state<'idle' | 'connecting' | 'open' | 'ended'>('idle'), note = $state('');
  let host: HTMLDivElement | undefined = $state();
  let sessions = $state<TerminalSessionInfo[]>([]);

  // Loaded only when a session starts: the section itself stays small.
  type Term = import('@xterm/xterm').Terminal;
  type Fit = import('@xterm/addon-fit').FitAddon;
  let term: Term | null = null, fit: Fit | null = null, ws: WebSocket | null = null, ro: ResizeObserver | null = null;
  let mock: { send: (b: Uint8Array) => void } | null = null;

  async function load() {
    try { st = await api.terminal(); err = ''; } catch (e) { err = e instanceof Error ? e.message : String(e); }
  }
  // One fetch at a time: a call during a fetch asks for one more run after it,
  // so the newest answer wins and requests never pile up.
  let inflight = false, again = false, loaded = $state(false);
  async function loadSessions() {
    if (inflight) { again = true; return; }
    inflight = true;
    try { sessions = (await api.terminalSessions()).sessions; loaded = true; } catch { sessions = []; } finally { inflight = false; }
    if (again) { again = false; loadSessions(); }
  }
  $effect(() => { load(); });
  $effect(() => { if (st?.enabled && st.available) loadSessions(); else sessions = []; });

  // Keep the list current while nothing is open here: sessions detach and end
  // on the server (a restart of the helper, another tab, another administrator).
  // A tick is skipped while a fetch is running.
  const POLL_MS = 5000;
  const live = $derived(!!(st?.enabled && st.available) && phase !== 'open' && phase !== 'connecting');
  let visible = $state(typeof document === 'undefined' || document.visibilityState === 'visible');
  $effect(() => {
    if (!live || !visible) return;
    const t = setInterval(() => { if (!inflight) { loadSessions(); load(); } }, POLL_MS);
    return () => clearInterval(t);
  });
  function onVisibility() {
    visible = document.visibilityState === 'visible';
    if (visible && live) { loadSessions(); load(); }
  }
  // The session of this tab survives a reload: its id is kept in sessionStorage
  // (per tab) and reattached on load, once the old page has detached.
  const saved = (): string => { try { return sessionStorage.getItem(SESSION_KEY) ?? ''; } catch { return ''; } };
  const save = (id: string) => { try { if (id) sessionStorage.setItem(SESSION_KEY, id); else sessionStorage.removeItem(SESSION_KEY); } catch { /* no storage */ } };
  let resumed = false, tries = 0;
  $effect(() => {
    if (resumed || !loaded || !st?.enabled || !st.available) return;
    const id = saved(), a = resumeAction(id, sessions);
    if (a === 'attach') { resumed = true; start(id); }
    else if (a === 'none' || a === 'clear' || tries >= 3) { resumed = true; save(''); }
    else {
      // Still shown as open in a window: the old page may not be detached yet.
      const t = setTimeout(() => { tries++; loadSessions(); }, 1000);
      return () => clearTimeout(t);
    }
  });
  // A new session has no id on the client yet: it is the one that appears as mine and open.
  async function remember(before: string[], sock: WebSocket) {
    for (const ms of [300, 1200, 3000]) {
      await new Promise((r) => setTimeout(r, ms));
      if (ws !== sock) return;
      try { const id = newSessionId(before, (await api.terminalSessions()).sessions); if (id) { save(id); return; } } catch { /* try again */ }
    }
  }
  // The server may mark the session detached just after the connection drops, so
  // look again a little later; Reattach then shows up without waiting for a tick.
  let late: ReturnType<typeof setTimeout>[] = [], gone = false;
  function refreshSoon() {
    late.forEach(clearTimeout);
    late = [1500, 4000].map((ms) => setTimeout(() => { loadSessions(); load(); }, ms));
  }

  async function toggle(on: boolean) {
    busy = true;
    try { st = await api.setTerminal(on); if (!on) stop(); toast(on ? 'Terminal turned on' : 'Terminal turned off', 'info'); } catch (e) { toastError(e); } finally { busy = false; }
  }

  function cleanup() {
    ro?.disconnect(); ro = null;
    term?.dispose(); term = null; fit = null;
    mock = null;
  }
  // end: tell the server to end the session for good (Disconnect, closing the
  // page). Without it the session is only detached and keeps running.
  function stop(end = true) {
    if (end) save('');
    const w = ws; ws = null;
    if (w) {
      w.onclose = null;
      if (end && w.readyState === WebSocket.OPEN) { try { w.send(endMsg()); } catch { /* closing anyway */ } }
      w.close(1000);
    }
    cleanup();
    if (phase === 'open' || phase === 'connecting') { phase = 'idle'; note = ''; }
    loadSessions();
    if (!gone) refreshSoon();
  }
  // Leaving this page inside the panel, closing or reloading it only detaches; the
  // session can be reattached (a reload in the same tab does it by itself).
  onDestroy(() => { gone = true; late.forEach(clearTimeout); stop(false); });
  function beforeUnload() { stop(false); }

  async function start(session?: string) {
    if (phase === 'connecting' || phase === 'open') return;
    phase = 'connecting'; note = '';
    try {
      const [{ Terminal }, { FitAddon }, t] = await Promise.all([import('@xterm/xterm'), import('@xterm/addon-fit'), api.terminalTicket(session)]);
      if (!host) { phase = 'idle'; return; }
      term = new Terminal({ cursorBlink: true, fontFamily: 'ui-monospace, Menlo, Consolas, monospace', fontSize: 14, scrollback: 2000, convertEol: false });
      fit = new FitAddon();
      term.loadAddon(fit);
      host.replaceChildren();
      term.open(host);
      fit.fit();
      const enc = new TextEncoder();
      const sendResize = () => { if (ws?.readyState === WebSocket.OPEN && term) ws.send(resizeMsg(term.rows, term.cols)); };

      if (t.ticket === 'mock') {
        mock = mockSession((b) => term?.write(b));
        term.onData((d) => mock?.send(enc.encode(d)));
        phase = 'open'; term.focus();
        return;
      }
      const sock = new WebSocket(wsURL(location, t.ticket, term.rows, term.cols));
      sock.binaryType = 'arraybuffer';
      ws = sock;
      sock.onopen = () => {
        phase = 'open'; term?.focus();
        if (session) save(session); else remember(sessions.map((x) => x.id), sock);
      };
      sock.onmessage = (ev) => { if (ev.data instanceof ArrayBuffer) term?.write(new Uint8Array(ev.data)); };
      sock.onclose = (ev) => {
        if (ws !== sock) return;
        ws = null;
        // Any end reason except a lost connection means the session is gone.
        if (ev.reason && ev.reason !== 'detach' && ev.reason !== 'error') save('');
        // Close code 1006 with no reason: the upgrade itself was refused.
        note = ev.reason ? endText(ev.reason) : phase === 'connecting' ? 'Could not open the terminal.' : 'Session closed.';
        phase = 'ended';
        cleanup();
        load();
        loadSessions();
        refreshSoon();
      };
      term.onData((d) => { if (sock.readyState === WebSocket.OPEN) sock.send(enc.encode(d)); });
      term.onBinary((d) => { if (sock.readyState === WebSocket.OPEN) sock.send(Uint8Array.from(d, (c) => c.charCodeAt(0))); });
      term.onResize(sendResize);
      ro = new ResizeObserver(() => { try { fit?.fit(); } catch { /* hidden */ } });
      ro.observe(host);
    } catch (e) {
      cleanup();
      phase = 'idle';
      if (session) save('');
      toastError(e);
      load();
      loadSessions();
    }
  }

  async function endSession(id: string) {
    try { await api.endTerminalSession(id); if (saved() === id) save(''); toast('Session ended', 'info'); } catch (e) { toastError(e); }
    loadSessions(); load();
  }
  const when = (t: number) => new Date(t * 1000).toLocaleString();
</script>

<svelte:window onbeforeunload={beforeUnload} />
<svelte:document onvisibilitychange={onVisibility} />

<section class="card space-y-3 p-4" aria-label="Terminal settings">
  <div class="flex items-center justify-between gap-3">
    <div>
      <h3 class="text-base font-semibold">Browser terminal</h3>
      <p class="text-sm text-muted">A login prompt on this node, in the browser. It asks for a Linux account name and password; no root shell is handed out. Administrators only. Keystrokes are never recorded; each session's start and end are in the audit log.</p>
    </div>
    {#if st}<Toggle checked={st.enabled} disabled={busy || !st.available} label="Allow the browser terminal" onchange={toggle} />{/if}
  </div>
  {#if err}
    <p class="text-sm text-danger" role="alert">{err}</p>
  {:else if !st}
    <p class="text-sm text-muted" role="status">Loading…</p>
  {:else if !st.available}
    <p class="text-sm text-muted">Not available: the helper on this system has no terminal support.</p>
  {:else}
    <p class="text-xs text-muted">Off by default. Closes after {st.idleMinutes} minutes without typing, at most {st.maxHours} hours, {st.maxSessions} sessions at a time ({st.sessions} open now). Turning it off ends open sessions.</p>
    <p class="text-xs text-muted">If the page disconnects, the session keeps running for 2 minutes, and for as long as <code>apt</code> or <code>dpkg</code> is running in it, so an nodespot update typed here finishes safely. Closing or reloading the page only disconnects it too: a reload reattaches by itself, otherwise reattach from the list below, or just wait. The Disconnect button ends the session.</p>
  {/if}
</section>

{#if st?.enabled && st.available}
  <section class="card space-y-3 p-4" aria-label="Terminal">
    <div class="flex flex-wrap items-center gap-3">
      <h3 class="mr-auto text-base font-semibold">Session</h3>
      {#if phase === 'open' || phase === 'connecting'}
        <button class="btn btn-sm" onclick={() => stop(true)}>Disconnect</button>
      {:else}
        <button class="btn btn-sm btn-primary" onclick={() => start()}>{phase === 'ended' ? 'Connect again' : 'Connect'}</button>
      {/if}
    </div>
    {#if note}<p class="text-sm text-muted" role="status">{note}</p>{/if}
    {#if phase === 'connecting'}<p class="text-sm text-muted" role="status">Connecting…</p>{/if}
    {#if sessions.length}
      <ul class="space-y-1 text-sm" aria-label="Open terminal sessions">
        {#each sessions as s (s.id)}
          <li class="flex flex-wrap items-center gap-2">
            <span class="mr-auto">{s.owner || 'unknown'}{s.linuxUser ? ` as ${s.linuxUser}` : ''}, started {when(s.started)}, {s.attached ? 'open in a window' : 'detached'}{s.mine ? '' : ' (another administrator)'}</span>
            {#if s.mine && !s.attached && phase !== 'open' && phase !== 'connecting'}<button class="btn btn-sm" onclick={() => start(s.id)}>Reattach</button>{/if}
            <button class="btn btn-sm" onclick={() => endSession(s.id)}>End</button>
          </li>
        {/each}
      </ul>
    {/if}
    <div bind:this={host} class="h-[26rem] overflow-hidden rounded border border-line bg-black p-1 {phase === 'open' || phase === 'connecting' ? '' : 'hidden'}" aria-label="Terminal screen"></div>
  </section>
{/if}
