<script lang="ts">
  import type { BroadStatus } from '../api';

  // StatusBadge — visual indicator for a stack's broad status, matching
  // ADR-0052 / PR-09's TUI mapping. Symbol + colour both come from the
  // broad value; everything else (size, surrounding layout) is the row's
  // problem.
  //
  // Colour comes from the embedded theme tokens via Tailwind classes
  // (text-dark-success, text-dark-warn, ...). Light mode is class-toggled
  // on <html> by the App component so the same component renders correctly
  // in either palette.

  interface Props {
    broad: BroadStatus | string;
  }

  const { broad }: Props = $props();

  // Mapping per ADR-0052 §"Sidebar tree". Falls through to the muted
  // "deleted" badge for any unrecognised value so a future schema bump
  // never blanks a row.
  interface BadgeStyle {
    symbol: string;
    classes: string;
    label: string;
  }

  const styles: Record<BroadStatus, BadgeStyle> = {
    deployed: {
      symbol: '●',
      classes: 'text-dark-success dark:text-dark-success',
      label: 'Deployed',
    },
    failed: {
      symbol: '⊘',
      classes: 'text-dark-error dark:text-dark-error',
      label: 'Failed',
    },
    partial: {
      symbol: '◐',
      classes: 'text-dark-warn dark:text-dark-warn',
      label: 'Partial — CFN failed but resources deployed',
    },
    drifted: {
      // No accent_alt mapping in tokens.go; warn (amber) sits closest to
      // the "orange" the ADR calls for and matches the TUI's amber-on-warn
      // rendering.
      symbol: '↯',
      classes: 'text-dark-warn dark:text-dark-warn',
      label: 'Drifted',
    },
    deploying: {
      symbol: '…',
      classes: 'text-dark-accent dark:text-dark-accent animate-pulse',
      label: 'Deploying',
    },
    draft: {
      symbol: '✎',
      classes: 'text-dark-muted dark:text-dark-muted',
      label: 'Draft',
    },
    deleted: {
      symbol: '⊗',
      classes: 'text-dark-muted/60 dark:text-dark-muted/60',
      label: 'Deleted',
    },
  };

  const style = $derived(styles[broad as BroadStatus] ?? styles.deleted);
</script>

<span
  class="inline-flex items-center justify-center w-3.5 text-[12px] leading-none shrink-0 {style.classes}"
  title={style.label}
  aria-label={style.label}
>
  {style.symbol}
</span>
