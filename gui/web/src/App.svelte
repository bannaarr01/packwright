<script lang="ts">
  import { onMount } from 'svelte';
  import { api, type SlashCommand, type ThemePayload } from './lib/api';
  import AIPanel from './lib/ai/AIPanel.svelte';
  import Launcher from './lib/launcher/Launcher.svelte';
  import Palette from './lib/palette/Palette.svelte';
  import RunPanel from './lib/run/RunPanel.svelte';
  import Sidebar from './lib/sidebar/Sidebar.svelte';
  import StackActionPanel from './lib/stack/StackActionPanel.svelte';
  import { stackPanel } from './lib/stack/stackPanel';
  import {
    Switcher,
    RegionSwitcher,
    type SwitchResult,
  } from './lib/profile-switcher';

  // Root component. Owns palette state, theme, AWS context, and sidebar
  // collapsed-ness. Sub components read those via props (one-way flow).
  let paletteOpen = $state(false);
  // running holds the command the user picked; non-null opens the run panel.
  let running = $state<SlashCommand | null>(null);
  // aiOpen toggles the AI assistant panel (opened from the sidebar).
  let aiOpen = $state(false);
  let theme = $state<ThemePayload | null>(null);
  let profile = $state('-');
  let region = $state('-');
  let account = $state('-');
  // The profile / region switcher overlays, opened from the sidebar footer pill.
  let profileSwitcherOpen = $state(false);
  let regionSwitcherOpen = $state(false);

  // applyResult refreshes the footer context from a successful switch. Both
  // switchers return the verified Identity, so the chip updates without a
  // round-trip to api.profile()/region()/account().
  function applyResult(res: SwitchResult) {
    if (!res.ok || !res.identity) return;
    profile = res.identity.profile || profile;
    region = res.identity.region || region;
    account = res.identity.account || account;
  }

  // Sidebar collapsed state — persisted across launches in localStorage so
  // the window opens the way the user left it. Defaults to expanded on a
  // fresh install.
  const COLLAPSED_KEY = 'packwright.sidebar.collapsed';
  let sidebarCollapsed = $state(false);

  // Mirror the TUI keymap: Cmd/Ctrl+K toggles the palette, Escape closes it,
  // Cmd/Ctrl+B toggles the sidebar (mirrors VS Code / Linear convention).
  function handleKeydown(e: KeyboardEvent) {
    const meta = e.metaKey || e.ctrlKey;
    if (meta && e.key.toLowerCase() === 'k') {
      e.preventDefault();
      paletteOpen = true;
      return;
    }
    if (meta && e.key.toLowerCase() === 'b') {
      e.preventDefault();
      toggleSidebar();
      return;
    }
    if (paletteOpen && e.key === 'Escape') {
      e.preventDefault();
      paletteOpen = false;
    }
  }

  function toggleSidebar() {
    sidebarCollapsed = !sidebarCollapsed;
    try {
      localStorage.setItem(COLLAPSED_KEY, sidebarCollapsed ? '1' : '0');
    } catch {
      // Private browsing or no storage available — collapsed state simply
      // resets next launch. Not worth surfacing.
    }
  }

  // runSlash opens the run panel for the picked command. The panel fetches the
  // command's form, collects inputs, and dispatches it through the engine
  // (RunSlashCommand) — replacing the old log-only SelectSlashCommand stub.
  function runSlash(sc: SlashCommand) {
    paletteOpen = false;
    running = sc;
  }

  // Svelte 5's onMount accepts either an async callback OR a sync callback
  // that returns a cleanup function — never both. bootstrap() runs to the
  // side so onMount stays sync.
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
      console.error('Packwright GUI: failed to initialise', err);
    }
  }

  onMount(() => {
    try {
      sidebarCollapsed = localStorage.getItem(COLLAPSED_KEY) === '1';
    } catch {
      sidebarCollapsed = false;
    }
    void bootstrap();
    window.addEventListener('keydown', handleKeydown);
    return () => window.removeEventListener('keydown', handleKeydown);
  });
</script>

<div
  class="h-full flex bg-light-bg text-light-fg dark:bg-dark-bg dark:text-dark-fg overflow-hidden"
>
  <Sidebar
    collapsed={sidebarCollapsed}
    {profile}
    {region}
    {account}
    onToggle={toggleSidebar}
    onRunSlash={runSlash}
    onOpenPalette={() => (paletteOpen = true)}
    onOpenAI={() => (aiOpen = true)}
    onOpenProfile={() => (profileSwitcherOpen = true)}
    onOpenRegion={() => (regionSwitcherOpen = true)}
  />

  <main class="flex-1 relative flex flex-col min-w-0">
    <!-- Drag rail across the top of the main canvas — completes the window
         drag zone the sidebar starts. Pure chrome; -webkit-app-region: drag
         is what lets the user actually move the window. -->
    <div
      class="h-9 shrink-0 flex items-center justify-end px-4 gap-3
             border-b border-light-border/40 dark:border-dark-border/40"
      style="-webkit-app-region: drag;"
    >
      <span class="text-[11px] text-dark-fg/40 font-mono select-none">
        Press
        <kbd
          class="px-1 py-0.5 rounded bg-dark-border/40 dark:bg-dark-border/40 text-[10px]"
          >⌘K</kbd
        >
        to invoke
      </span>
    </div>

    <!-- Canvas. Dot grid carries the negative space and keeps the cmdk box
         from feeling marooned. -->
    <section
      class="flex-1 relative overflow-hidden bg-dot-grid"
    >
      <Launcher />
      {#if paletteOpen}
        <Palette onClose={() => (paletteOpen = false)} onPick={runSlash} />
      {/if}
      {#if running}
        <RunPanel slash={running.slash} title={running.title} onClose={() => (running = null)} />
      {/if}
      {#if $stackPanel}
        <!-- Keyed so switching to a different stack remounts the panel with
             fresh state rather than carrying over the previous flow. -->
        {#key $stackPanel.stack}
          <StackActionPanel target={$stackPanel} onClose={() => stackPanel.set(null)} />
        {/key}
      {/if}
      {#if aiOpen}
        <AIPanel onClose={() => (aiOpen = false)} />
      {/if}
      {#if profileSwitcherOpen}
        <Switcher
          onClose={() => (profileSwitcherOpen = false)}
          onSwitched={applyResult}
        />
      {/if}
      {#if regionSwitcherOpen}
        <RegionSwitcher
          active={region}
          onClose={() => (regionSwitcherOpen = false)}
          onSwitched={applyResult}
        />
      {/if}
    </section>
  </main>
</div>

{#if !theme}
  <!-- onMount hasn't finished yet — render nothing visible above the body
       background so the user doesn't see a flash of unstyled content. -->
{/if}
