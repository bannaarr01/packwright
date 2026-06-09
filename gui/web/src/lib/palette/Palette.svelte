<script lang="ts">
  import { onMount } from 'svelte';
  import { EventsOn } from '../../wailsjs/wailsjs/runtime/runtime';
  import { api, type SlashCommand } from '../api';

  // cmdk-style fuzzy command palette. Mirrors the TUI's palette.go:
  //   - opens via Cmd/Ctrl+K (handled in App.svelte)
  //   - filters fuzzy over slash + title
  //   - Enter selects, Esc closes (when filter empty), arrows navigate
  //
  // The data source is api.listSlashCommands(), which reads from the pack
  // registry (pack.LoadPalette) on the Go side. The Go bridge also emits
  // `packwright:palette-changed` whenever a manifest under one of the
  // watched roots is edited — we subscribe to it here so the palette
  // refreshes live without the user having to close + reopen.

  interface Props {
    onClose: () => void;
  }

  const { onClose }: Props = $props();

  let query = $state('');
  let items = $state<SlashCommand[]>([]);
  let activeIndex = $state(0);
  let inputEl: HTMLInputElement | undefined = $state();
  // Diagnostic: capture the last fetch outcome so we can see it in the
  // window without needing DevTools or stderr capture. Remove after the
  // GUI palette is confirmed wired end-to-end.
  let debugStatus = $state<string>('idle');

  async function refresh() {
    try {
      const fresh = await api.listSlashCommands();
      items = fresh ?? [];
      debugStatus = `loaded ${items.length} rows`;
    } catch (err) {
      debugStatus = `ERROR: ${err instanceof Error ? err.message : String(err)}`;
      console.error('Packwright GUI: listSlashCommands failed', err);
    }
  }

  onMount(() => {
    refresh();
    inputEl?.focus();
    // EventsOn returns an unregister function. Wails' runtime always exists
    // when this component renders inside the webview; the cleanup is still
    // important so a closing palette stops re-fetching in the background.
    const off = EventsOn('packwright:palette-changed', () => {
      refresh();
    });
    return () => off?.();
  });

  // fuzzyScore returns a non-negative score when every character of `q`
  // appears in `text` in order. Lower scores mean a tighter match (fewer
  // skipped characters). -1 means no match. The algorithm is intentionally
  // small — the seed set is two items today and at most a few dozen even
  // once the pack registry is wired in.
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
    items
      .map((sc) => ({
        sc,
        score: fuzzyScore(`${sc.slash} ${sc.title}`, query),
      }))
      .filter((r) => r.score >= 0)
      .sort((a, b) => a.score - b.score)
      .map((r) => r.sc),
  );

  // Keep activeIndex in range when the filtered list shrinks.
  $effect(() => {
    if (activeIndex >= filtered.length) {
      activeIndex = Math.max(0, filtered.length - 1);
    }
  });

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
      const pick = filtered[activeIndex];
      if (pick) {
        api.selectSlashCommand(pick).catch((err) => {
          console.error('Packwright GUI: SelectSlashCommand failed', err);
        });
        onClose();
      }
      return;
    }
    if (e.key === 'Escape' && query === '') {
      // Escape with an empty filter closes the palette; with a non-empty
      // filter we let the input clear naturally on the next keystroke.
      e.preventDefault();
      onClose();
    }
  }
</script>

<!-- Backdrop. Clicking it closes the palette. -->
<button
  type="button"
  class="absolute inset-0 bg-black/40 cursor-default"
  aria-label="Close palette"
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
    placeholder="Type a command…"
    class="w-full px-4 py-3 bg-transparent outline-none border-b border-light-border dark:border-dark-border"
    autocomplete="off"
    spellcheck="false"
  />
  <div class="px-4 py-1 text-xs opacity-50 border-b border-light-border dark:border-dark-border">
    debug: {debugStatus}
  </div>
  <ul class="max-h-80 overflow-y-auto py-1" role="listbox">
    {#each filtered as item, i (item.slash + '|' + item.title)}
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
          onclick={() => {
            api.selectSlashCommand(item).catch((err) => {
              console.error('Packwright GUI: SelectSlashCommand failed', err);
            });
            onClose();
          }}
        >
          <span class="font-mono text-sm">{item.slash}</span>
          <span class="opacity-70 text-sm">{item.title}</span>
        </button>
      </li>
    {:else}
      <li class="px-4 py-3 opacity-60 text-sm">No matches</li>
    {/each}
  </ul>
</div>
