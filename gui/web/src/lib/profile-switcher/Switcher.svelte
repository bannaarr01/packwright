<script lang="ts">
  import { onMount } from 'svelte';
  import {
    switcherApi,
    type ProfileEntry,
    type SwitchResult,
  } from './index';

  // The /profile switcher panel. Behaviour mirrors the TUI screen + the
  // existing cmdk palette (Palette.svelte):
  //   - opens when the parent toggles its open state
  //   - filters fuzzy over profile name + region
  //   - Enter (or click) selects and triggers SwitchProfile
  //   - Esc with empty filter closes
  //
  // The data source is switcherApi.list() (gui/bindings_profile.go::ListProfiles).
  // On success we dwell on the result for ~200ms so the user sees the new
  // identity before the panel disappears — the parent header is expected to
  // refresh either by reading the SwitchResult returned to it, or by
  // subscribing to the Wails event keyed by PROFILE_SWITCHED_EVENT.

  interface Props {
    onClose: () => void;
    onSwitched?: (result: SwitchResult) => void;
  }

  const { onClose, onSwitched }: Props = $props();

  let profiles = $state<ProfileEntry[]>([]);
  let query = $state('');
  let activeIndex = $state(0);
  let switching = $state(false);
  let result = $state<SwitchResult | null>(null);
  let inputEl: HTMLInputElement | undefined = $state();
  let loadError = $state<string | null>(null);

  onMount(async () => {
    try {
      profiles = await switcherApi.list();
      const idx = profiles.findIndex((p) => p.active);
      if (idx >= 0) activeIndex = idx;
    } catch (err) {
      loadError = String(err);
      // surface to the console too — see Palette.svelte for precedent.
      console.error('Packwright GUI: ListProfiles failed', err);
    }
    inputEl?.focus();
  });

  // fuzzyScore is the same scorer used by Palette.svelte. Kept inline rather
  // than promoted to a shared module so each lib stays self-contained.
  function fuzzyScore(text: string, q: string): number {
    if (q === '') return 0;
    const t = text.toLowerCase();
    const needle = q.toLowerCase();
    let ti = 0;
    let skipped = 0;
    for (const ch of needle) {
      const found = t.indexOf(ch, ti);
      if (found === -1) return -1;
      skipped += found - ti;
      ti = found + 1;
    }
    return skipped;
  }

  const filtered = $derived(
    profiles
      .map((p) => ({
        p,
        score: fuzzyScore(`${p.name} ${p.region ?? ''}`, query),
      }))
      .filter((r) => r.score >= 0)
      .sort((a, b) => a.score - b.score)
      .map((r) => r.p),
  );

  $effect(() => {
    if (activeIndex >= filtered.length) {
      activeIndex = Math.max(0, filtered.length - 1);
    }
  });

  async function pick(entry: ProfileEntry) {
    if (switching) return;
    switching = true;
    result = null;
    try {
      const res = await switcherApi.switch(entry.name, entry.region);
      result = res;
      if (res.ok) {
        onSwitched?.(res);
        // Brief dwell so the user sees the new identity confirmation before
        // the panel disappears. Long enough for a visual ack, short enough
        // to comfortably meet the "header updates within 1s" DoD.
        setTimeout(onClose, 250);
      }
    } catch (err) {
      result = { ok: false, error: String(err) };
    } finally {
      switching = false;
    }
  }

  function handleKeydown(e: KeyboardEvent) {
    if (e.key === 'ArrowDown') {
      e.preventDefault();
      if (filtered.length > 0) {
        activeIndex = (activeIndex + 1) % filtered.length;
      }
      return;
    }
    if (e.key === 'ArrowUp') {
      e.preventDefault();
      if (filtered.length > 0) {
        activeIndex = (activeIndex - 1 + filtered.length) % filtered.length;
      }
      return;
    }
    if (e.key === 'Enter') {
      e.preventDefault();
      const pickItem = filtered[activeIndex];
      if (pickItem) void pick(pickItem);
      return;
    }
    if (e.key === 'Escape' && query === '') {
      e.preventDefault();
      onClose();
    }
  }
</script>

<!-- Backdrop. Clicking it closes the panel — same affordance as Palette.svelte. -->
<button
  type="button"
  class="absolute inset-0 bg-black/40 cursor-default"
  aria-label="Close profile switcher"
  onclick={onClose}
></button>

<div
  class="absolute left-1/2 top-24 -translate-x-1/2 w-[min(640px,90%)] rounded-lg shadow-2xl
         bg-light-bg dark:bg-dark-bg
         border border-light-border dark:border-dark-border
         text-light-fg dark:text-dark-fg overflow-hidden"
>
  <input
    bind:this={inputEl}
    bind:value={query}
    onkeydown={handleKeydown}
    placeholder="Switch AWS profile…"
    class="w-full px-4 py-3 bg-transparent outline-none border-b border-light-border dark:border-dark-border disabled:opacity-60"
    autocomplete="off"
    spellcheck="false"
    disabled={switching}
  />
  <ul class="max-h-80 overflow-y-auto py-1" role="listbox" aria-label="AWS profiles">
    {#each filtered as item, i (item.name)}
      <li role="presentation">
        <button
          type="button"
          role="option"
          aria-selected={i === activeIndex}
          tabindex="-1"
          class="w-full text-left px-4 py-2 cursor-pointer flex items-baseline gap-3"
          class:bg-light-selection_bg={i === activeIndex}
          class:dark:bg-dark-selection_bg={i === activeIndex}
          class:text-light-selection_fg={i === activeIndex}
          class:dark:text-dark-selection_fg={i === activeIndex}
          onmouseenter={() => (activeIndex = i)}
          onclick={() => pick(item)}
          disabled={switching}
        >
          <span class="font-mono text-sm w-4">{item.active ? '→' : ''}</span>
          <span class="font-mono text-sm">{item.name}</span>
          <span class="opacity-70 text-sm">{item.region || '(no region)'}</span>
        </button>
      </li>
    {:else}
      {#if loadError}
        <li class="px-4 py-3 text-sm text-light-error dark:text-dark-error">
          {loadError}
        </li>
      {:else}
        <li class="px-4 py-3 opacity-60 text-sm">
          No profiles found in ~/.aws — configure the AWS CLI first.
        </li>
      {/if}
    {/each}
  </ul>

  {#if result && !result.ok}
    <div
      class="px-4 py-3 border-t border-light-border dark:border-dark-border bg-light-bg/60 dark:bg-dark-bg/60"
      role="alert"
    >
      <div class="text-sm text-light-error dark:text-dark-error">
        {result.error}
      </div>
      {#if result.suggested && result.suggested.length > 0}
        <div class="text-xs opacity-80 mt-2">Try:</div>
        <ul class="text-xs font-mono mt-1 space-y-1">
          {#each result.suggested as cmd}
            <li>$ {cmd}</li>
          {/each}
        </ul>
      {/if}
    </div>
  {/if}

  {#if result && result.ok && result.identity}
    <div
      class="px-4 py-3 border-t border-light-border dark:border-dark-border bg-light-bg/60 dark:bg-dark-bg/60 text-sm"
      role="status"
    >
      <span class="font-mono">{result.identity.profile}</span>
      <span class="opacity-60 mx-2">·</span>
      <span class="font-mono">{result.identity.account}</span>
      <span class="opacity-60 mx-2">·</span>
      <span class="font-mono text-xs opacity-80">{result.identity.arn}</span>
    </div>
  {/if}
</div>
