<script lang="ts">
  // The Now Calling strip: a full-width button under the header. Plain display props only.
  import ChevronRight from '@lucide/svelte/icons/chevron-right';
  import Clock from '@lucide/svelte/icons/clock';
  let { mode, label, source = '', main = '', secs = '', idle = '', onclick }: {
    mode: 'talking' | 'idle'; label: string;
    /** Talking: the source line ("AllStar", "DMR · TG 91"), the main line and the elapsed "mm:ss". Idle: the one-line text. */
    source?: string; main?: string; secs?: string; idle?: string; onclick: () => void;
  } = $props();
</script>

<button type="button" {onclick} aria-label={label} aria-haspopup="dialog"
  class="nc-strip block w-full text-left border-t {mode === 'talking' ? 'nc-tx border-transparent' : 'border-line bg-surface text-muted hover:text-fg'}">
  <span class="mx-auto flex max-w-[1100px] items-center gap-3 px-3 sm:px-4 h-[52px] {mode === 'talking' ? 'py-1.5' : ''}">
    {#if mode === 'talking'}
      <span class="nc-dot shrink-0" aria-hidden="true"></span>
      <span class="min-w-0 flex-1">
        <span class="block truncate text-[11px] font-semibold uppercase tracking-wide opacity-90">Now calling · {source}</span>
        <span class="block truncate text-[15px] font-bold leading-tight">{main}</span>
      </span>
      <span class="shrink-0 font-mono text-sm font-semibold tabular-nums">{secs}</span>
      <ChevronRight size={18} class="shrink-0" aria-hidden="true" />
    {:else}
      <Clock size={15} class="shrink-0" aria-hidden="true" />
      <span class="min-w-0 flex-1 truncate text-[13px]">{idle}</span>
      <ChevronRight size={16} class="shrink-0" aria-hidden="true" />
    {/if}
  </span>
</button>

<style>
  .nc-tx { background: var(--row-tx); color: var(--row-fg); }
  .nc-strip:focus-visible { outline: 2px solid var(--accent); outline-offset: -3px; }
  .nc-tx:focus-visible { outline-color: #fff; }
  .nc-dot { width: 10px; height: 10px; border-radius: 9999px; background: #fff; }
  @media (prefers-reduced-motion: no-preference) {
    .nc-dot { animation: nc-pulse 1.4s ease-in-out infinite; }
  }
  @keyframes nc-pulse { 0%, 100% { opacity: 1; transform: scale(1); } 50% { opacity: .45; transform: scale(.7); } }
</style>
