<script lang="ts">
  import { app } from '../lib/store.svelte';
  import Wifi from '@lucide/svelte/icons/wifi';
  // Shown when this page was opened over the setup hotspot (open WiFi, plain HTTP).
</script>
{#if app.hotspot}
  <div class="border-b border-warn/50 bg-warn/10 px-3 py-2 text-sm" role="alert">
    <div class="mx-auto flex max-w-[1100px] items-start gap-2">
      <Wifi size={16} class="mt-0.5 shrink-0 text-warn" />
      <p><strong class="text-warn">You are connected through the setup hotspot.</strong> This WiFi network is open and the page is not encrypted, so anyone in range can read what you type here, including your password. Use it to add the node to your WiFi, then change your password if you signed in this way.</p>
    </div>
  </div>
{/if}
