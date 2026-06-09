<script lang="ts">
  // Idle-state landing for the canvas. The cmdk palette is the primary
  // input — this view exists to (1) tell a first-time user what ⌘K does
  // and (2) give the canvas a visual anchor when no palette is open.
  import logoUrl from '../../assets/logo.svg';
</script>

<div class="absolute inset-0 flex items-center justify-center pointer-events-none">
  <div class="text-center space-y-6 max-w-md px-6">
    <!-- Wordmark — slightly faded so it sits with the grid rather than on top of it. -->
    <img src={logoUrl} alt="Packwright" class="mx-auto w-40 h-auto opacity-90" />

    <div class="space-y-3">
      <p class="text-[13px] text-dark-fg/55 font-mono tracking-tight">
        Scaffold and manage AWS infrastructure templates.
      </p>

      <p class="text-[12.5px] text-dark-fg/50 flex items-center justify-center gap-2">
        <span>Press</span>
        <kbd
          class="px-2 py-0.5 rounded-md font-mono text-[11px]
                 bg-dark-border/35 border border-dark-border/70
                 shadow-[inset_0_-1px_0_rgba(0,0,0,0.35)]"
        >
          ⌘K
        </kbd>
        <span>to open the palette,</span>
        <kbd
          class="px-2 py-0.5 rounded-md font-mono text-[11px]
                 bg-dark-border/35 border border-dark-border/70
                 shadow-[inset_0_-1px_0_rgba(0,0,0,0.35)]"
        >
          ⌘B
        </kbd>
        <span>to toggle the sidebar.</span>
      </p>
    </div>
  </div>
</div>
