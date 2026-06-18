<script lang="ts">
  import { grouping, type Grouping } from './grouping';

  // GroupingToggle — segmented control flipping the sidebar between the
  // Projects grouping (default, top of the tree) and the By-pack grouping
  // (today's behaviour). The store persists the choice to localStorage so
  // a refresh remembers it.

  const options: Array<{ value: Grouping; label: string }> = [
    { value: 'projects', label: 'Projects' },
    { value: 'by-pack', label: 'By pack' },
  ];
</script>

<div
  class="flex items-center gap-0.5 p-0.5 rounded-md bg-dark-border/25
         border border-dark-border/30 text-[11px] font-mono"
  role="group"
  aria-label="Sidebar grouping"
>
  {#each options as opt (opt.value)}
    {@const active = $grouping === opt.value}
    <button
      type="button"
      class={[
        'flex-1 px-2 py-0.5 rounded-[5px] transition text-center',
        active ? 'bg-dark-bg text-dark-fg' : 'text-dark-fg/55 hover:text-dark-fg',
      ].join(' ')}
      aria-pressed={active}
      onclick={() => grouping.set(opt.value)}
    >
      {opt.label}
    </button>
  {/each}
</div>
