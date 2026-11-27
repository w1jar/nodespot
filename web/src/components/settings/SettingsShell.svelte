<script lang="ts">
  import { router } from '../../lib/router.svelte';
  import { app, isAdmin } from '../../lib/store.svelte';
  import { settingsRoute } from '../../lib/sysnav';
  import { groups, defaultFor, visibleGroups, visibleTabs } from './sections';

  const admin = $derived(isAdmin());
  const openable = $derived(visibleGroups(groups, admin));
  const route = $derived(
    settingsRoute(router.path, openable.map((g) => ({ id: g.id, tabs: (g.tabs ?? []).map((t) => t.id) })), defaultFor(admin), admin)
      ?? { group: defaultFor(admin), tab: null },
  );
  const grp = $derived(openable.find((g) => g.id === route.group) ?? openable[0]);
  const tabs = $derived(visibleTabs(grp));
  const tab = $derived(grp.tabs?.find((t) => t.id === route.tab));
  // Updates carries the same badge as the header, on the System group and on its Updates tab.
  const updCount = $derived((app.updates.nodespot || (app.system?.updateAvailable ?? false) ? 1 : 0) + app.updates.os);
  const hasBadge = $derived(admin && (updCount > 0 || app.updates.reboot));
  // Keep the active button and tab visible in the one-line rows on phones.
  $effect(() => {
    void route.group; void route.tab;
    queueMicrotask(() => document.querySelectorAll('nav[aria-label^="Settings groups"] [aria-current], nav[aria-label$=" sections"] [aria-current]')
      .forEach((el) => el.scrollIntoView({ block: 'nearest', inline: 'center' })));
  });
  const badgeClass = $derived(app.updates.security > 0 || app.updates.reboot ? 'bg-warn' : 'bg-accent');
</script>

<div class="space-y-4">
  <!-- Two bars: groups as buttons, then (tabbed groups only) a tinted strip of underlined tabs. Each row is one line and scrolls sideways on phones. -->
  <div class="border-b border-line pb-4">
    <nav class="no-scrollbar flex gap-1.5 overflow-x-auto" aria-label="Settings groups">
      {#each openable as g (g.id)}
        <a href={'#/settings/' + g.id} aria-current={g.id === grp.id ? 'page' : undefined}
          class="btn shrink-0 whitespace-nowrap {g.id === grp.id ? 'btn-accent-outline' : ''}">
          {g.label}{#if g.id === 'system' && hasBadge}<span class="size-2 rounded-full {badgeClass}" aria-label="updates available"></span>{/if}
        </a>
      {/each}
    </nav>
    {#if grp.tabs}
      <nav class="no-scrollbar mt-2 flex gap-1 overflow-x-auto rounded border border-line bg-surface-2 px-1.5" aria-label="{grp.label} sections">
        {#each tabs as t (t.id)}
          <a href={'#/settings/' + grp.id + '/' + t.id} aria-current={t.id === route.tab ? 'page' : undefined}
            class="-mb-px inline-flex min-h-[1.9rem] shrink-0 items-center gap-1.5 whitespace-nowrap border-b-2 px-2.5 text-[13px] font-medium pointer-coarse:min-h-11 transition-colors {t.id === route.tab ? 'border-accent text-accent' : 'border-transparent text-muted hover:text-fg'}">
            {t.label}{#if t.id === 'updates' && hasBadge}<span class="size-2 rounded-full {badgeClass}" aria-label="updates available"></span>{/if}
          </a>
        {/each}
      </nav>
    {/if}
  </div>
  <section class="min-w-0 space-y-3" aria-label={grp.label}>
    {#if grp.tabs}
      {#if tab}
        {#key tab.id}
          {#await tab.load() then m}<m.default {...(tab.props ?? {})} />{:catch}<p class="text-sm text-danger">Could not load this page. Reload the page.</p>{/await}
        {/key}
      {/if}
    {:else}
      {#each grp.pages ?? [] as load, i (i)}
        {#await load() then m}<m.default />{:catch}<p class="text-sm text-danger">Could not load this page. Reload the page.</p>{/await}
      {/each}
    {/if}
  </section>
</div>
