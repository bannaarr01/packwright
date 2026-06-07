<script lang="ts">
  // The MVP-1 launcher screen. Mirrors the TUI's launcher.go: a short
  // instruction line pointing at the palette keybinding. Real launcher
  // content (recent packs, quick actions) arrives in a later PR.
  import logoUrl from '../../assets/logo.svg';
</script>

<div class="h-full flex items-center justify-center">
  <div class="text-center space-y-4">
    <img src={logoUrl} alt="Packwright" class="mx-auto w-40 h-auto" />
    <div class="opacity-70">
      Press <kbd
        class="px-1.5 py-0.5 rounded font-mono text-sm bg-light-border/40 dark:bg-dark-border/40"
        >⌘K</kbd
      >
      to open the command palette.
    </div>
  </div>
</div>
