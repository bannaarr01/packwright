<script lang="ts">
  import { onMount } from 'svelte';
  import { EventsOn } from '../../wailsjs/wailsjs/runtime/runtime';
  import { api, type Project, type StackRow } from '../api';
  import ProjectGroup from './ProjectGroup.svelte';

  // ProjectsView — top-level container for the Projects grouping mode.
  // Owns the workspace fetch (ListProjects + ListStacks per env) and
  // re-fetches whenever the packwright:workspace-changed event fires.
  //
  // Stacks are stored in a single Map keyed by `<projectSlug>/<envSlug>`
  // so children render with no cross-component coordination. Each
  // ProjectGroup looks up its own slices via the same key scheme.

  interface Props {
    query: string;
    collapsed: boolean;
  }

  const { query, collapsed }: Props = $props();

  let projects = $state<Project[]>([]);
  let stacks = $state<Map<string, StackRow[]>>(new Map());

  // Sequence counter guards against two workspace-changed events landing
  // close enough together that the older refresh()'s stacks Promise.all
  // resolves last and overwrites the newer result. Each refresh() bumps
  // the counter; any await that finishes after a newer call bails out.
  let refreshSeq = 0;

  async function refresh() {
    const seq = ++refreshSeq;
    try {
      const fresh = (await api.listProjects()) ?? [];
      // Fetch every (project, env) in parallel so a wide tree does not
      // serialise sidebar load behind round-trips. ListStacks already
      // degrades to an empty slice on read failure, so a single bad env
      // does not blank the rest.
      const results = await Promise.all(
        fresh.flatMap((p) =>
          p.envs.map(async (e): Promise<[string, StackRow[]]> => {
            const key = `${p.slug}/${e.slug}`;
            try {
              return [key, (await api.listStacks(p.slug, e.slug)) ?? []];
            } catch (err) {
              console.error('Packwright GUI: sidebar listStacks failed', p.slug, e.slug, err);
              return [key, []];
            }
          }),
        ),
      );
      if (seq !== refreshSeq) return;
      // Assign both reactive states in the same tick so the tree does not
      // briefly render with the new projects but stale (empty) stacks.
      projects = fresh;
      stacks = new Map(results);
    } catch (err) {
      console.error('Packwright GUI: sidebar listProjects failed', err);
    }
  }

  onMount(() => {
    refresh();
    const off = EventsOn('packwright:workspace-changed', () => refresh());
    return () => off?.();
  });

  // When the query matches no stack in any project we still render the
  // project headers so the user knows the tree exists — the count drops
  // to zero, which is information by itself. Mirrors how the existing
  // by-pack grouping handles empty matches.
</script>

{#if projects.length === 0}
  {#if !collapsed}
    <div class="px-3 py-6 text-center text-[12px] text-dark-fg/40">
      No projects yet.
    </div>
  {/if}
{:else if collapsed}
  <!-- Collapsed sidebar: render only the project initials so the column
       still communicates that projects exist without claiming the width
       to lay out the full tree. -->
  <ul class="space-y-1 mt-1">
    {#each projects as project (project.slug)}
      <li
        class="w-7 h-7 mx-auto grid place-items-center rounded-md
               border border-dark-border/40 font-mono text-[10px] text-dark-fg/70 uppercase"
        title={project.name || project.slug}
      >
        {(project.name || project.slug).slice(0, 2)}
      </li>
    {/each}
  </ul>
{:else}
  {#each projects as project (project.slug)}
    <ProjectGroup {project} {stacks} {query} />
  {/each}
{/if}
