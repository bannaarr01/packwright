<script lang="ts">
  import type { StackRow } from '../api';
  import StatusBadge from './StatusBadge.svelte';

  // StackRow — one stack inside an env group. Renders the broad-status
  // badge, the stack name (mono, the user's identity for the row), and the
  // last-updated timestamp as a muted suffix when present.
  //
  // No click handler in this PR — opening a stack-detail screen lands in a
  // later PR (the TUI's record screen is PR-09; the GUI's parallel is
  // queued). The row is rendered as a button anyway so future wiring is a
  // one-line change and keyboard focus works today.

  interface Props {
    row: StackRow;
  }

  const { row }: Props = $props();

  const updated = $derived.by(() => {
    if (!row.updated_at) return '';
    // Render the date portion only — full RFC3339 is too noisy in a row of
    // sidebar width. Falls back to the raw string if parsing fails so the
    // user still sees something rather than an empty muted span.
    const t = Date.parse(row.updated_at);
    if (Number.isNaN(t)) return row.updated_at;
    return new Date(t).toISOString().slice(0, 10);
  });
</script>

<button
  type="button"
  class="w-full flex items-center gap-2 px-2 py-[5px] rounded-md
         text-left hover:bg-dark-border/30 group transition"
  title={`${row.slash}  ${row.name}`}
>
  <StatusBadge broad={row.broad} />
  <span class="font-mono text-[12.5px] text-dark-fg/90 truncate min-w-0 flex-1">
    {row.name}
  </span>
  {#if updated}
    <span class="font-mono text-[10.5px] text-dark-fg/35 shrink-0">{updated}</span>
  {/if}
</button>
