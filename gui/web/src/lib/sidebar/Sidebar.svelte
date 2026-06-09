<script lang="ts">
  import { onMount } from 'svelte';
  import { EventsOn } from '../../wailsjs/wailsjs/runtime/runtime';
  import { api, type SlashCommand } from '../api';
  import logoIconUrl from '../../assets/logo-icon.svg';

  // Sidebar — the browsable surface for Packwright's major functions. The
  // palette (⌘K) remains the canonical entry point; this is a discovery
  // affordance for users who want to see what their pack registry contains
  // without typing first.
  //
  // Data comes from the same Go binding the palette uses (api.listSlashCommands)
  // — the Go side already groups by source. We re-subscribe to the
  // packwright:palette-changed event so a manifest edit refreshes both
  // surfaces atomically.

  interface Props {
    collapsed: boolean;
    profile: string;
    region: string;
    account: string;
    onToggle: () => void;
    onRunSlash: (sc: SlashCommand) => void;
    onOpenPalette: () => void;
  }

  const { collapsed, profile, region, account, onToggle, onRunSlash, onOpenPalette }: Props =
    $props();

  let items = $state<SlashCommand[]>([]);
  let query = $state('');
  let openSections = $state<Record<string, boolean>>({
    user: true,
    packs: true,
    wizards: true,
  });

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

  // Group rows the way the user actually scans them: user-scope commands
  // first, then packs (each pack as its own subhead), then the trailing
  // wizards. Inside each group rows preserve LoadPalette's order, which
  // already honours pin promotion + alphabetical fallback.
  interface Group {
    key: string;
    label: string;
    rows: SlashCommand[];
  }

  const grouped = $derived.by<Group[]>(() => {
    const q = query.trim().toLowerCase();
    const match = (r: SlashCommand) =>
      q === '' || r.slash.toLowerCase().includes(q) || r.title.toLowerCase().includes(q);

    const user: SlashCommand[] = [];
    const wizards: SlashCommand[] = [];
    const packMap = new Map<string, SlashCommand[]>();

    for (const r of items) {
      if (!match(r)) continue;
      if (r.source === 'builtin') {
        wizards.push(r);
      } else if (r.scope === 'user') {
        user.push(r);
      } else {
        const bucket = packMap.get(r.source) ?? [];
        bucket.push(r);
        packMap.set(r.source, bucket);
      }
    }

    const groups: Group[] = [];
    if (user.length) groups.push({ key: 'user', label: 'Commands', rows: user });
    for (const [name, rows] of [...packMap].sort(([a], [b]) => a.localeCompare(b))) {
      groups.push({ key: `pack:${name}`, label: name, rows });
    }
    if (wizards.length) groups.push({ key: 'wizards', label: 'Wizards', rows: wizards });
    return groups;
  });

  function toggleSection(key: string) {
    openSections[key] = !(openSections[key] ?? true);
  }

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

  <!-- Quick actions: surface the two scaffold wizards as primary entry
       points. They're the one-click "I want to start" affordances. -->
  <div class="px-3 pb-3 space-y-1">
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
       footer stays pinned. -->
  <nav class="flex-1 min-h-0 overflow-y-auto px-2 pb-3 scrollbar-hairline">
    {#if grouped.length === 0}
      {#if !collapsed}
        <div class="px-3 py-6 text-center text-[12px] text-dark-fg/40">
          {query ? 'No matches.' : 'No commands yet.'}
        </div>
      {/if}
    {:else}
      {#each grouped as group (group.key)}
        {@const open = openSections[group.key] ?? true}
        <div class="mb-2">
          {#if !collapsed}
            <button
              type="button"
              class="w-full flex items-center justify-between px-2 py-1 text-[10.5px]
                     uppercase tracking-micro text-dark-fg/40 hover:text-dark-fg/70 transition"
              onclick={() => toggleSection(group.key)}
            >
              <span class="font-mono">{group.label}</span>
              <span class="flex items-center gap-2">
                <span class="opacity-60">{group.rows.length}</span>
                <svg
                  width="9"
                  height="9"
                  viewBox="0 0 16 16"
                  class="transition-transform"
                  class:rotate-180={!open}
                  fill="none"
                  stroke="currentColor"
                  stroke-width="2"
                >
                  <path d="M4 6l4 4 4-4" stroke-linecap="round" stroke-linejoin="round" />
                </svg>
              </span>
            </button>
          {/if}
          {#if open || collapsed}
            <ul class="space-y-0.5 mt-0.5">
              {#each group.rows as row (group.key + '|' + row.slash + '|' + row.title)}
                <li>
                  <button
                    type="button"
                    class="w-full flex items-center gap-2.5 px-2 py-[5px] rounded-md
                           text-left hover:bg-dark-border/30 group transition"
                    onclick={() => onRunSlash(row)}
                    title={`${row.slash}  ${row.title}`}
                  >
                    {#if collapsed}
                      <span
                        class="w-7 h-7 shrink-0 grid place-items-center rounded-md
                               border border-dark-border/40 group-hover:border-dark-border
                               font-mono text-[10px] text-dark-fg/70 uppercase"
                      >
                        {row.slash.replace(/^\//, '').slice(0, 2)}
                      </span>
                    {:else}
                      <span class="w-1 h-1 rounded-full bg-dark-fg/25 group-hover:bg-dark-accent shrink-0 transition"></span>
                      <span
                        class="font-mono text-[12.5px] text-dark-fg/90 truncate"
                        >{row.slash}</span
                      >
                      <span class="text-[12px] text-dark-fg/45 truncate flex-1">{row.title}</span>
                      {#if row.pinned}
                        <span
                          class="font-mono text-[9px] text-dark-accent border border-dark-accent/40
                                 rounded px-1 py-0.5 leading-none shrink-0"
                          title="Pinned default"
                        >
                          PIN
                        </span>
                      {/if}
                    {/if}
                  </button>
                </li>
              {/each}
            </ul>
          {/if}
        </div>
      {/each}
    {/if}
  </nav>

  <!-- Footer pill: AWS context. Mono everything because it's read like a
       prompt line. -->
  <div class="px-3 py-3 border-t border-dark-border/40 shrink-0">
    {#if collapsed}
      <div
        class="w-8 h-8 mx-auto grid place-items-center rounded-md bg-dark-border/30
               font-mono text-[10px] text-dark-fg/70"
        title={`${profile} · ${region} · ${account}`}
      >
        {profile.slice(0, 2).toUpperCase()}
      </div>
    {:else}
      <div
        class="px-2.5 py-2 rounded-md bg-dark-border/20 border border-dark-border/40
               font-mono text-[11px] leading-tight"
      >
        <div class="flex items-center gap-2">
          <span class="w-1.5 h-1.5 rounded-full bg-dark-accent shrink-0"></span>
          <span class="text-dark-fg/90 truncate">{profile}</span>
        </div>
        <div class="text-dark-fg/55 pl-3.5 truncate">{region} · {account}</div>
      </div>
    {/if}
  </div>
</aside>
