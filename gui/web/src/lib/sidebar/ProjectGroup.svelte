<script lang="ts">
  import type { Project, StackRow } from '../api';
  import StackRowComponent from './StackRow.svelte';

  // ProjectGroup — one collapsible Project containing zero or more envs,
  // each containing the stacks recorded under that env. Stack rows are
  // fetched by the parent ProjectsView and handed in as a map keyed by
  // `<projectSlug>/<envSlug>` so re-renders are cheap and re-fetching
  // happens in one place.
  //
  // Collapsed state is local to the component. We deliberately do not
  // persist it across launches: the project tree changes shape often
  // enough that yesterday's "Acme/dev was open" preference is rarely
  // worth restoring, and it would mean another localStorage key per
  // project. Defaults to open.

  interface Props {
    project: Project;
    stacks: Map<string, StackRow[]>;
    query: string;
  }

  const { project, stacks, query }: Props = $props();

  let projectOpen = $state(true);
  let openEnvs = $state<Record<string, boolean>>({});

  function envOpen(envSlug: string): boolean {
    return openEnvs[envSlug] ?? true;
  }
  function toggleEnv(envSlug: string) {
    openEnvs[envSlug] = !envOpen(envSlug);
  }

  function envKey(envSlug: string): string {
    return `${project.slug}/${envSlug}`;
  }

  // Filter the row list against the sidebar's filter query. Match on both
  // the stack name and the slash command — the same affordance the
  // existing pack grouping offers on its rows.
  function filterStacks(rows: StackRow[]): StackRow[] {
    const q = query.trim().toLowerCase();
    if (q === '') return rows;
    return rows.filter(
      (r) => r.name.toLowerCase().includes(q) || r.slash.toLowerCase().includes(q),
    );
  }

  // Total visible stack count across envs after filtering — drives the
  // count badge on the project header and lets ProjectsView hide a
  // project that has zero matches without prop-drilling state back up.
  const visibleCount = $derived.by(() => {
    let n = 0;
    for (const env of project.envs) {
      n += filterStacks(stacks.get(envKey(env.slug)) ?? []).length;
    }
    return n;
  });
</script>

<div class="mb-3">
  <button
    type="button"
    class="w-full flex items-center justify-between px-2 py-1 text-[10.5px]
           uppercase tracking-micro text-dark-fg/40 hover:text-dark-fg/70 transition"
    onclick={() => (projectOpen = !projectOpen)}
  >
    <span class="font-mono truncate">{project.name || project.slug}</span>
    <span class="flex items-center gap-2 shrink-0">
      <span class="opacity-60">{visibleCount}</span>
      <svg
        width="9"
        height="9"
        viewBox="0 0 16 16"
        class="transition-transform"
        class:rotate-180={!projectOpen}
        fill="none"
        stroke="currentColor"
        stroke-width="2"
      >
        <path d="M4 6l4 4 4-4" stroke-linecap="round" stroke-linejoin="round" />
      </svg>
    </span>
  </button>

  {#if projectOpen}
    {#if project.envs.length === 0}
      <div class="px-3 py-1.5 text-[11.5px] text-dark-fg/35 italic">
        No envs yet.
      </div>
    {:else}
      <ul class="space-y-1 mt-1">
        {#each project.envs as env (env.slug)}
          {@const rows = filterStacks(stacks.get(envKey(env.slug)) ?? [])}
          {@const open = envOpen(env.slug)}
          <li>
            <button
              type="button"
              class="w-full flex items-center justify-between pl-3 pr-2 py-0.5
                     text-[10px] uppercase tracking-micro text-dark-fg/35
                     hover:text-dark-fg/60 transition"
              onclick={() => toggleEnv(env.slug)}
            >
              <span class="font-mono truncate">{env.name || env.slug}</span>
              <span class="flex items-center gap-2 shrink-0">
                <span class="opacity-70">{rows.length}</span>
                <svg
                  width="7"
                  height="7"
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
            {#if open}
              <ul class="pl-3 space-y-0.5 mt-0.5">
                {#if rows.length === 0}
                  <li class="px-2 py-0.5 text-[11px] text-dark-fg/30 italic">
                    No stacks yet.
                  </li>
                {:else}
                  {#each rows as row (row.name)}
                    <li>
                      <StackRowComponent {row} />
                    </li>
                  {/each}
                {/if}
              </ul>
            {/if}
          </li>
        {/each}
      </ul>
    {/if}
  {/if}
</div>
