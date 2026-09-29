<script lang="ts">
  import icons from "virtual:app-icons";
  import { hue, initials } from "../lib/format";

  let { icon = "", name, size = 32 }: { icon?: string; name: string; size?: number } = $props();
  const data = $derived(icon ? icons[icon] : undefined);
</script>

{#if data}
  <svg class="app-icon" width={size} height={size} viewBox="-3 -3 30 30" role="img" aria-label={name}>
    <rect x="-3" y="-3" width="30" height="30" rx="7" fill="#{data[0]}" opacity="0.15" />
    <path d={data[1]} fill="#{data[0]}" />
  </svg>
{:else}
  <span
    class="app-icon letters"
    style="width:{size}px;height:{size}px;font-size:{Math.round(size * 0.4)}px;background:hsl({hue(name)} 55% 45%)"
    aria-label={name}>{initials(name)}</span
  >
{/if}

<style>
  .app-icon {
    flex: none;
    border-radius: 7px;
  }
  .letters {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    color: #fff;
    font-weight: 700;
  }
</style>
