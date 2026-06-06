<script lang="ts">
  import { onMount } from 'svelte';
  import { api, type ThemePayload } from './lib/api';
  import Launcher from './lib/launcher/Launcher.svelte';
  import Palette from './lib/palette/Palette.svelte';

  // Root component. Owns the palette-open state and the resolved theme; sub
  // components read those via props instead of any global store so the data
  // flow stays one-directional.
  let paletteOpen = $state(false);
  let theme = $state<ThemePayload | null>(null);
  let profile = $state('-');
  let region = $state('-');
  let account = $state('-');

  // Mirror the TUI's keymap: Cmd+K (macOS) or Ctrl+K (else) opens the
  // palette, Escape closes it. Cmd/Ctrl+C is left to the OS so users can
  // still copy text from the window chrome.
  function handleKeydown(e: KeyboardEvent) {
    const meta = e.metaKey || e.ctrlKey;
    if (meta && e.key.toLowerCase() === 'k') {
      e.preventDefault();
      paletteOpen = true;
      return;
    }
    if (paletteOpen && e.key === 'Escape') {
      e.preventDefault();
      paletteOpen = false;
    }
  }

  // Svelte 5's onMount accepts either an async callback OR a sync callback
  // that returns a cleanup function — never both. bootstrap() runs off to
  // the side so onMount can stay sync and return the keydown listener
  // cleanup.
  async function bootstrap() {
    try {
      const [t, p, r, a] = await Promise.all([
        api.theme(),
        api.profile(),
        api.region(),
        api.account(),
      ]);
      theme = t;
      profile = p;
      region = r;
      account = a;
      document.documentElement.classList.toggle('dark', t.mode === 'dark');
    } catch (err) {
      // Surface RPC failures in the console so a developer running outside
      // the Wails runtime sees a clear pointer rather than a silent blank
      // window.
      console.error('Packwright GUI: failed to initialise', err);
    }
  }

  onMount(() => {
    void bootstrap();
    window.addEventListener('keydown', handleKeydown);
    return () => window.removeEventListener('keydown', handleKeydown);
  });
</script>

<div class="h-full flex flex-col bg-light-bg text-light-fg dark:bg-dark-bg dark:text-dark-fg">
  <header
    class="flex items-center justify-between px-6 py-3 border-b border-light-border dark:border-dark-border select-none"
  >
    <div class="font-mono text-sm">
      <span class="text-light-accent dark:text-dark-accent">packwright</span>
      <span class="opacity-60 mx-2">·</span>
      <span>{profile}</span>
      <span class="opacity-60 mx-2">·</span>
      <span>{region}</span>
      <span class="opacity-60 mx-2">·</span>
      <span>{account}</span>
    </div>
    <div class="text-xs opacity-60">
      Press <kbd class="px-1 py-0.5 rounded bg-light-border/40 dark:bg-dark-border/40">⌘K</kbd> or
      <kbd class="px-1 py-0.5 rounded bg-light-border/40 dark:bg-dark-border/40">Ctrl+K</kbd>
    </div>
  </header>

  <main class="flex-1 relative">
    <Launcher />
    {#if paletteOpen}
      <Palette onClose={() => (paletteOpen = false)} />
    {/if}
  </main>
</div>

{#if !theme}
  <!-- onMount hasn't finished yet — render nothing visible above the body
       background so the user doesn't see a flash of unstyled content. -->
{/if}
