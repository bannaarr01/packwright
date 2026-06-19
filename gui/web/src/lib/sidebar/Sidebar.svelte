<script lang="ts">
  import { onMount } from 'svelte';
  import { EventsOn } from '../../wailsjs/wailsjs/runtime/runtime';
  import { api, type SlashCommand } from '../api';
  import logoIconUrl from '../../assets/logo-icon.svg';
  import { grouping } from './grouping';
  import GroupingToggle from './GroupingToggle.svelte';
  import IndependentGroup from './IndependentGroup.svelte';
  import ProjectsView from './ProjectsView.svelte';

  // Sidebar — the browsable surface for Packwright's major functions. The
  // palette (⌘K) remains the canonical entry point; this is a discovery
  // affordance for users who want to see what their pack registry and
  // workspace contain without typing first.
  //
  // PR-10 splits the body in two:
  //   - Projects mode (default): ProjectsView renders the Project → Env →
  //     Stack tree, badged with the broad status from each record.
  //   - By-pack mode: IndependentGroup renders today's user / pack /
  //     wizards grouping, unchanged.
  //
  // The grouping store is backed by localStorage so the user's choice
  // survives refresh. The two views own their own data fetches; this
  // component holds only the surrounding chrome (drag rail, brand,
  // search, quick actions, footer) and the slash-command list that the
  // quick-action wizard buttons need.

  interface Props {
    collapsed: boolean;
    profile: string;
    region: string;
    account: string;
    onToggle: () => void;
    onRunSlash: (sc: SlashCommand) => void;
    onOpenPalette: () => void;
    onOpenAI: () => void;
    onOpenProfile: () => void;
    onOpenRegion: () => void;
  }

  const {
    collapsed,
    profile,
    region,
    account,
    onToggle,
    onRunSlash,
    onOpenPalette,
    onOpenAI,
    onOpenProfile,
    onOpenRegion,
  }: Props = $props();

  // SlashCommand list lives here (instead of inside IndependentGroup)
  // because the wizard quick-action buttons in the sidebar chrome also
  // need it. Sharing a single fetch keeps the palette-changed event from
  // firing two re-fetches per manifest edit.
  let items = $state<SlashCommand[]>([]);
  let query = $state('');

  async function refresh() {
    try {
      const fresh = await api.listSlashCommands();
      items = fresh ?? [];
    } catch (err) {
      console.error('Packwright GUI: sidebar listSlashCommands failed', err);
    }
  }

  onMount(() => {
    refresh();
    const off = EventsOn('packwright:palette-changed', () => refresh());
    return () => off?.();
  });

  function runWizard(slash: '/new-command' | '/new-pack') {
    const w = items.find((r) => r.slash === slash && r.source === 'builtin');
    if (w) onRunSlash(w);
  }
</script>

<aside
  class="sidebar-collapse-transition shrink-0 flex flex-col border-r border-light-border/70 dark:border-dark-border/70 bg-light-bg dark:bg-dark-bg"
  class:w-[268px]={!collapsed}
  class:w-[56px]={collapsed}
>
  <!-- Drag rail. The 80px left padding clears macOS traffic lights from
       TitleBarHiddenInset; without -webkit-app-region: drag on some frontend
       zone the window cannot be moved at all. -->
  <div
    class="h-9 shrink-0 pl-[80px] pr-2 flex items-center justify-end"
    style="-webkit-app-region: drag;"
  >
    <button
      type="button"
      class="opacity-50 hover:opacity-100 text-dark-fg/80 text-sm leading-none p-1 rounded
             hover:bg-dark-border/30 transition"
      style="-webkit-app-region: no-drag;"
      onclick={onToggle}
      aria-label={collapsed ? 'Expand sidebar' : 'Collapse sidebar'}
      title={collapsed ? 'Expand sidebar' : 'Collapse sidebar'}
    >
      {#if collapsed}
        <!-- chevron right -->
        <svg width="14" height="14" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.6">
          <path d="M6 4l4 4-4 4" stroke-linecap="round" stroke-linejoin="round" />
        </svg>
      {:else}
        <!-- chevron left -->
        <svg width="14" height="14" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.6">
          <path d="M10 4L6 8l4 4" stroke-linecap="round" stroke-linejoin="round" />
        </svg>
      {/if}
    </button>
  </div>

  <!-- Brand. The accent dot is the only colour saturation in the chrome —
       carries the GitHub-green from the theme tokens. -->
  <div class="px-4 pb-3 flex items-center gap-2.5">
    <span
      class="inline-block w-2 h-2 rounded-full bg-dark-accent shadow-[0_0_10px_rgba(126,231,135,0.55)]"
      aria-hidden="true"
    ></span>
    {#if !collapsed}
      <div class="flex items-center gap-2 min-w-0">
        <img src={logoIconUrl} alt="" class="h-5 w-auto opacity-90" />
        <span
          class="font-mono text-[13px] tracking-tight text-dark-fg/90 truncate"
          >packwright</span
        >
      </div>
    {/if}
  </div>

  <!-- Search. Doubles as a discovery shortcut: tapping Enter on a populated
       query opens the palette with the same string so the user can keep
       typing without re-entering it. -->
  {#if !collapsed}
    <div class="px-3 pb-3">
      <div
        class="flex items-center gap-2 px-2.5 py-1.5 rounded-md
               bg-dark-border/25 border border-transparent
               focus-within:border-dark-accent_alt/50 focus-within:bg-dark-border/40 transition"
      >
        <svg width="13" height="13" viewBox="0 0 16 16" class="opacity-50 shrink-0" fill="none" stroke="currentColor" stroke-width="1.5">
          <circle cx="7" cy="7" r="4.5" />
          <path d="M11 11l3.2 3.2" stroke-linecap="round" />
        </svg>
        <input
          type="text"
          bind:value={query}
          placeholder="Filter…"
          class="flex-1 bg-transparent outline-none text-[12.5px] placeholder:text-dark-fg/35"
          onkeydown={(e) => {
            if (e.key === 'Enter' && query.trim()) {
              onOpenPalette();
            }
          }}
        />
        <kbd
          class="font-mono text-[10px] opacity-50 px-1 py-0.5 rounded border border-dark-border/70"
          >⌘K</kbd
        >
      </div>
    </div>
  {/if}

  <!-- Grouping toggle: chooses which body view renders below. Hidden when
       the sidebar is collapsed since neither label fits in the 56px rail. -->
  {#if !collapsed}
    <div class="px-3 pb-3">
      <GroupingToggle />
    </div>
  {/if}

  <!-- Quick actions: surface the two scaffold wizards plus the AI assistant
       as primary entry points. They're the one-click "I want to start"
       affordances. -->
  <div class="px-3 pb-3 space-y-1">
    <button
      type="button"
      class="w-full flex items-center gap-3 px-2 py-1.5 rounded-md text-left
             hover:bg-dark-border/30 transition group"
      onclick={onOpenAI}
      title="AI assistant"
    >
      <span
        class="w-7 h-7 shrink-0 grid place-items-center rounded-md
               border border-dark-border/70 group-hover:border-dark-accent/60 transition"
      >
        <svg width="13" height="13" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5">
          <path d="M2.5 4.5A1.5 1.5 0 014 3h8a1.5 1.5 0 011.5 1.5v5A1.5 1.5 0 0112 11H6l-3 2.5V11H4a1.5 1.5 0 01-1.5-1.5v-5z" stroke-linejoin="round" />
        </svg>
      </span>
      {#if !collapsed}
        <span class="text-[13px]">AI assistant</span>
      {/if}
    </button>
    <button
      type="button"
      class="w-full flex items-center gap-3 px-2 py-1.5 rounded-md text-left
             hover:bg-dark-border/30 transition group"
      onclick={() => runWizard('/new-command')}
      title="New command"
    >
      <span
        class="w-7 h-7 shrink-0 grid place-items-center rounded-md
               border border-dark-border/70 group-hover:border-dark-accent/60 transition"
      >
        <svg width="12" height="12" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.6">
          <path d="M8 3v10M3 8h10" stroke-linecap="round" />
        </svg>
      </span>
      {#if !collapsed}
        <span class="text-[13px]">New command</span>
      {/if}
    </button>
    <button
      type="button"
      class="w-full flex items-center gap-3 px-2 py-1.5 rounded-md text-left
             hover:bg-dark-border/30 transition group"
      onclick={() => runWizard('/new-pack')}
      title="New pack"
    >
      <span
        class="w-7 h-7 shrink-0 grid place-items-center rounded-md
               border border-dark-border/70 group-hover:border-dark-accent/60 transition"
      >
        <svg width="12" height="12" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.6">
          <path d="M2.5 5.5L8 2.5l5.5 3M2.5 5.5v5L8 13.5l5.5-3v-5M2.5 5.5L8 8.5l5.5-3M8 8.5v5" stroke-linejoin="round" stroke-linecap="round" />
        </svg>
      </span>
      {#if !collapsed}
        <span class="text-[13px]">New pack</span>
      {/if}
    </button>
  </div>

  <div class="h-px mx-3 bg-dark-border/40 mb-2" aria-hidden="true"></div>

  <!-- Sections list. The whole region scrolls if it overflows; the sidebar
       footer stays pinned. The body view is chosen by the grouping store —
       Projects (the workspace tree) or By-pack (today's grouping). -->
  <nav class="flex-1 min-h-0 overflow-y-auto px-2 pb-3 scrollbar-hairline">
    {#if $grouping === 'projects'}
      <ProjectsView {query} {collapsed} />
    {:else}
      <IndependentGroup {items} {query} {collapsed} {onRunSlash} />
    {/if}
  </nav>

  <!-- Footer pill: AWS context. Mono everything because it's read like a
       prompt line. The profile and region segments are buttons that open the
       profile / region switchers; the account stays read-only. -->
  <div class="px-3 py-3 border-t border-dark-border/40 shrink-0">
    {#if collapsed}
      <button
        type="button"
        class="w-8 h-8 mx-auto grid place-items-center rounded-md bg-dark-border/30
               font-mono text-[10px] text-dark-fg/70 hover:text-dark-fg
               focus:outline-none focus-visible:ring-1 focus-visible:ring-dark-accent"
        title={`${profile} · ${region} · ${account} — switch profile`}
        onclick={onOpenProfile}
      >
        {profile.slice(0, 2).toUpperCase()}
      </button>
    {:else}
      <div
        class="px-2.5 py-2 rounded-md bg-dark-border/20 border border-dark-border/40
               font-mono text-[11px] leading-tight"
      >
        <button
          type="button"
          class="flex items-center gap-2 w-full text-left rounded text-dark-fg/90
                 hover:text-dark-fg focus:outline-none focus-visible:ring-1 focus-visible:ring-dark-accent"
          title="Switch AWS profile"
          onclick={onOpenProfile}
        >
          <span class="w-1.5 h-1.5 rounded-full bg-dark-accent shrink-0"></span>
          <span class="truncate">{profile}</span>
        </button>
        <div class="pl-3.5 truncate text-dark-fg/55">
          <button
            type="button"
            class="rounded hover:text-dark-fg focus:outline-none focus-visible:ring-1 focus-visible:ring-dark-accent"
            title="Switch AWS region"
            onclick={onOpenRegion}
          >{region}</button> · {account}
        </div>
      </div>
    {/if}
  </div>
</aside>
