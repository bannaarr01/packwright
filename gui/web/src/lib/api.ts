// Typed wrapper around the Wails bindings.
//
// Wails generates JS shims into ./wailsjs/ at build time (via `wails build` or
// `wails dev`); those shims are not present in the source tree on a fresh
// checkout. To avoid forcing every developer to run `wails generate` before
// they can typecheck, this module talks to `window.go.gui.App.*` directly and
// provides the typed shape itself. The Go-side binding signatures live in
// gui/bindings.go and must be kept in sync — there is exactly one place to
// look in each language.

export interface SlashCommand {
  slash: string;
  title: string;
}

export interface ThemeTokens {
  bg: string;
  fg: string;
  muted: string;
  accent: string;
  accent_alt: string;
  warn: string;
  error: string;
  success: string;
  border: string;
  selection_bg: string;
  selection_fg: string;
}

export interface ThemePayload {
  mode: 'dark' | 'light';
  tokens: ThemeTokens;
}

interface WailsBindings {
  Profile(): Promise<string>;
  Region(): Promise<string>;
  Account(): Promise<string>;
  ListSlashCommands(): Promise<SlashCommand[]>;
  Theme(): Promise<ThemePayload>;
  SelectSlashCommand(sc: SlashCommand): Promise<void>;
}

declare global {
  interface Window {
    go?: { gui?: { App?: WailsBindings } };
  }
}

// bindings reaches the Wails App methods at runtime. It throws if Wails has
// not finished mounting yet; the UI guards against that by awaiting Theme()
// inside its onMount before rendering anything that depends on it.
function bindings(): WailsBindings {
  const api = window.go?.gui?.App;
  if (!api) {
    throw new Error(
      'Wails bindings not ready — window.go.gui.App is undefined. ' +
        'Are you running outside the Wails runtime?',
    );
  }
  return api;
}

export const api = {
  profile: () => bindings().Profile(),
  region: () => bindings().Region(),
  account: () => bindings().Account(),
  listSlashCommands: () => bindings().ListSlashCommands(),
  theme: () => bindings().Theme(),
  selectSlashCommand: (sc: SlashCommand) => bindings().SelectSlashCommand(sc),
};
