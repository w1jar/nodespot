<script lang="ts">
  import { ApiError, api } from '../lib/api';
  import { app, refreshSchedules } from '../lib/store.svelte';
  import { toast, toastError } from '../lib/toast.svelte';
  import { DAY_NAMES, DAY_ORDER, conflictMessage, emptyInput, findConflicts, validateSchedule } from '../lib/schedules';
  import { modeTitle, pickerModes, targetLabel } from '../lib/modes';
  import { settings } from '../lib/settings.svelte';
  import type { Schedule, ScheduleInput } from '../lib/types';
  import Dialog from './Dialog.svelte';

  let { open = $bindable(false), schedule = null }: { open: boolean; schedule?: Schedule | null } = $props();
  let f = $state<ScheduleInput>(emptyInput(''));
  let repeat = $state<'weekly' | 'once'>('weekly');
  let busy = $state(false), error = $state('');
  // Hidden modes are not offered, except the one an edited schedule already uses (and the active mode, the default).
  const modes = $derived(pickerModes(app.modes, settings.hiddenModes, [schedule?.mode ?? '', ...[app.node?.active ?? ''].filter((n) => app.modes.some((m) => m.name === n && m.configured))].filter(Boolean)));
  const info = $derived(app.modes.find((m) => m.name === f.mode));
  const connect = $derived(f.action === 'connect');
  let wasOpen = false;
  $effect(() => {
    if (open && !wasOpen) {
      error = ''; busy = false;
      if (schedule) {
        const { id: _i, lastRun: _l, lastResult: _r, ...rest } = schedule;
        f = { ...rest, days: [...rest.days] };
        repeat = schedule.date ? 'once' : 'weekly';
      } else {
        f = emptyInput(app.node?.active || modes[0]?.name || '');
        f.days = [2]; repeat = 'weekly';
      }
    }
    wasOpen = open;
  });
  function toggleDay(d: number) { f.days = f.days.includes(d) ? f.days.filter((x) => x !== d) : [...f.days, d].sort((a, b) => a - b); }
  function body(): ScheduleInput {
    const c = f.action === 'connect';
    return {
      name: f.name.trim(), enabled: f.enabled, mode: f.mode, target: f.target.trim(), action: f.action,
      days: repeat === 'weekly' ? [...f.days].sort((a, b) => a - b) : [], date: repeat === 'once' ? f.date : '',
      start: f.start, end: c ? f.end : '',
    };
  }
  /** Overlap check against the saved schedules (the server checks again and answers 409). */
  const clash = $derived.by(() => {
    if (!open) return '';
    const c = findConflicts(body(), app.schedules?.schedules ?? [], schedule?.id);
    return c.length ? conflictMessage(c) : '';
  });
  async function save(e: Event) {
    e.preventDefault();
    const b = body();
    error = validateSchedule(b, modes.map((m) => m.name));
    if (error || clash) return;
    busy = true;
    try {
      if (schedule) await api.updateSchedule(schedule.id, b); else await api.addSchedule(b);
      await refreshSchedules();
      toast(schedule ? 'Schedule updated' : 'Schedule added');
      open = false;
    } catch (x) {
      if (x instanceof ApiError && x.code === 'schedule_conflict') { error = `This schedule ${x.message}.`; refreshSchedules().catch(() => {}); }
      else { error = x instanceof Error ? x.message : String(x); toastError(x); }
    } finally { busy = false; }
  }
  const chk = 'size-4 accent-[var(--accent)]';
</script>
<Dialog bind:open title={schedule ? 'Edit schedule' : 'Add schedule'} wide>
  <form id="schedform" class="space-y-3" onsubmit={save}>
    <div class="grid gap-3 sm:grid-cols-2">
      <div class="sm:col-span-2"><label class="label" for="sname">Name <span class="font-normal">(optional, for example the net's name)</span></label><input id="sname" class="input" maxlength="60" bind:value={f.name} placeholder="Tuesday net" /></div>
      <div><label class="label" for="smode">Mode</label>
        <select id="smode" class="input" bind:value={f.mode}>{#each modes as m (m.name)}<option value={m.name}>{m.title || modeTitle(m.name)}</option>{/each}</select></div>
      <div><label class="label" for="saction">Action</label>
        <select id="saction" class="input" bind:value={f.action}><option value="connect">Connect</option><option value="disconnect">Disconnect</option></select></div>
      <div class="sm:col-span-2"><label class="label" for="starget">{targetLabel(info)}{#if !connect} <span class="font-normal">(optional)</span>{/if}</label><input id="starget" class="input font-mono" bind:value={f.target} autocomplete="off" /></div>
    </div>
    <fieldset class="space-y-2 rounded border border-line p-2">
      <legend class="px-1 text-xs font-medium text-muted">Repeat</legend>
      <div class="flex gap-1.5" role="group" aria-label="Repeat">
        <button type="button" class="btn btn-sm {repeat === 'weekly' ? 'btn-primary' : ''}" aria-pressed={repeat === 'weekly'} onclick={() => (repeat = 'weekly')}>Weekly</button>
        <button type="button" class="btn btn-sm {repeat === 'once' ? 'btn-primary' : ''}" aria-pressed={repeat === 'once'} onclick={() => (repeat = 'once')}>Once</button>
      </div>
      {#if repeat === 'weekly'}
        <div class="flex flex-wrap gap-1" role="group" aria-label="Days of the week">
          {#each DAY_ORDER as d}
            <button type="button" class="btn btn-sm min-w-10 {f.days.includes(d) ? 'btn-primary' : ''}" aria-pressed={f.days.includes(d)} onclick={() => toggleDay(d)}>{DAY_NAMES[d]}</button>
          {/each}
        </div>
      {:else}
        <div class="max-w-48"><label class="label" for="sdate">Date</label><input id="sdate" type="date" class="input" bind:value={f.date} /></div>
      {/if}
      <div class="flex flex-wrap gap-3">
        <div><label class="label" for="sstart">{connect ? 'Start' : 'Time'}</label><input id="sstart" type="time" class="input w-32" bind:value={f.start} required /></div>
        {#if connect}
          <div><label class="label" for="send">End <span class="font-normal">(optional)</span></label>
            <div class="flex items-center gap-1"><input id="send" type="time" class="input w-32" bind:value={f.end} />{#if f.end}<button type="button" class="btn btn-sm" onclick={() => (f.end = '')}>Clear</button>{/if}</div></div>
        {/if}
      </div>
      <p class="text-xs text-muted">Times are node time{app.schedules?.timezone ? ` (${app.schedules.timezone})` : ''}.{#if connect}{' '}An end time disconnects the link then{f.end && f.end < f.start ? '; this window ends the next day' : ''}.{/if}</p>
    </fieldset>
    <label class="inline-flex cursor-pointer items-center gap-1.5 text-sm"><input type="checkbox" class={chk} bind:checked={f.enabled} />Enabled</label>
    {#if clash}<p class="text-sm text-warn" role="alert">This schedule {clash}. The node has one radio and one active mode, so two windows cannot overlap: change the time or the day.</p>{/if}
    {#if error}<p class="text-sm text-danger" role="alert">{error}</p>{/if}
  </form>
  {#snippet footer()}
    <button class="btn" onclick={() => (open = false)}>Cancel</button>
    <button class="btn btn-primary" type="submit" form="schedform" disabled={busy || !!clash}>{busy ? 'Saving…' : 'Save'}</button>
  {/snippet}
</Dialog>
