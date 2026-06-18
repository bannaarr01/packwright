<script lang="ts">
  import type { SlashCommand } from '../api';

  // IndependentGroup — the pre-PR-10 sidebar body. Renders the palette's
  // discoverable surface grouped by provenance: user-scope commands first,
  // then packs (each pack as its own subhead), then the trailing wizards.
  //
  // Lifted from Sidebar.svelte unchanged so the "By pack" grouping mode
  // preserves today's behaviour byte-for-byte (per PR-10's rule: do not
  // remove today's grouping logic — wrap it). Sidebar.svelte still owns
  // the SlashCommand fetch + the packwright:palette-changed subscription
  // because the wizard quick-action buttons sit in the chrome around the
  // groups and need the same data — keeping a single fetch path avoids
  // a double subscription per manifest edit.

  interface Props {
    items: SlashCommand[];
    query: string;
    collapsed: boolean;
    onRunSlash: (sc: SlashCommand) => void;
  }

  const { items, query, collapsed, onRunSlash }: Props = $props();

  let openSections = $state<Record<string, boolean>>({
    user: true,
    packs: true,
    wizards: true,
  });

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
</script>

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
                  <span
                    class="w-1 h-1 rounded-full bg-dark-fg/25 group-hover:bg-dark-accent shrink-0 transition"
                  ></span>
                  <span class="font-mono text-[12.5px] text-dark-fg/90 truncate">{row.slash}</span>
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
