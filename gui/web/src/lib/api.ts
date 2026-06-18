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
  // Provenance of the row. `"user"` for the user scope, `"builtin"` for the
  // wizard manifests, otherwise the pack name. The sidebar groups on this.
  source: string;
  // `"user"` or `"pack"` — mirrors pack.Scope on the Go side.
  scope: string;
  // True for the row promoted to first by Config.PinnedDefaults inside a
  // colliding slash group.
  pinned: boolean;
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

// Workspace tree shapes mirror gui/bindings.go (Project / Env). The DTOs are
// deliberately a trimmed view of internal/workspace types — only what the
// Projects-grouping sidebar renders.
export interface Env {
  slug: string;
  name: string;
}

export interface Project {
  slug: string;
  name: string;
  envs: Env[];
}

// BroadStatus mirrors internal/record.BroadStatus. The set is closed; the
// StatusBadge component falls back to a muted "deleted" badge for anything
// outside the set so a future schema bump never blanks a row.
export type BroadStatus =
  | 'draft'
  | 'deploying'
  | 'deployed'
  | 'partial'
  | 'failed'
  | 'drifted'
  | 'deleted';

// StackRow is the sidebar row shape for one persisted stack record. Mirrors
// gui/bindings.go:StackRow — small payload, no resources or outputs.
export interface StackRow {
  name: string;
  slash: string;
  broad: BroadStatus | string;
  // RFC3339 (UTC). Empty string when the record has no deployed_at or
  // last_updated_at — drafts mostly. The sidebar hides the timestamp in
  // that case rather than rendering "Invalid Date".
  updated_at: string;
}

// Exported so wails-app.d.ts can intersect it with ProfileBindings into the
// single canonical Window.go.gui.App typing.
export interface WailsBindings {
  Profile(): Promise<string>;
  Region(): Promise<string>;
  Account(): Promise<string>;
  ListSlashCommands(): Promise<SlashCommand[]>;
  Theme(): Promise<ThemePayload>;
  SelectSlashCommand(sc: SlashCommand): Promise<void>;
  ListProjects(): Promise<Project[]>;
  ListStacks(project: string, env: string): Promise<StackRow[]>;
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
  listProjects: () => bindings().ListProjects(),
  listStacks: (project: string, env: string) => bindings().ListStacks(project, env),
};
