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

// FormField mirrors gui/bindings_run.go:FormField — one input the run panel
// renders before dispatching a manifest command.
export interface FormField {
  id: string;
  label: string;
  type: string;
  placeholder: string;
  required: boolean;
}

// FormPayload mirrors gui/bindings_run.go:FormPayload. resolved is false when
// the slash has no backing manifest, so the run panel declines to open.
export interface FormPayload {
  slash: string;
  title: string;
  resolved: boolean;
  fields: FormField[];
}

// RunResult mirrors gui/bindings_run.go:RunResult — the collected engine
// output plus the run outcome.
export interface RunResult {
  ok: boolean;
  output: string[];
  error: string;
}

// The following mirror gui/bindings_stack.go — the MVP-7 stack actions
// (update / scale / delete) over internal/update + internal/scaling.

// DiffRow is one resource change in a change-set diff.
export interface DiffRow {
  action: string; // add | modify | replace | delete
  logical_id: string;
  resource_type: string;
  replacement: string; // "True" | "False" | "Conditional" | ""
  iam: boolean;
  causes: string[];
}

// ParamRow is one parameter delta.
export interface ParamRow {
  key: string;
  old: string;
  new: string;
  caused_replacement: boolean;
}

// DiffPayload mirrors gui/bindings_stack.go:DiffPayload.
export interface DiffPayload {
  adds: DiffRow[];
  modifies: DiffRow[];
  replaces: DiffRow[];
  deletes: DiffRow[];
  params: ParamRow[];
  no_changes: boolean;
}

// StackActionResult mirrors gui/bindings_stack.go:StackActionResult. outcome is
// one of "executed" | "no_changes" | "needs_confirm" | "notice" | "error".
// needs_confirm means the change would replace resources (or a scale hit an env
// guard): show confirm_reason + diff, then re-call the same action with confirm.
export interface StackActionResult {
  ok: boolean;
  outcome: string;
  notice: string;
  needs_confirm: boolean;
  confirm_reason: string;
  diff?: DiffPayload;
  output: string[];
  error: string;
}

// ScalingTarget is one editable scaling parameter, pre-filled with its current
// value. Mirrors gui/bindings_stack.go:ScalingTarget.
export interface ScalingTarget {
  param: string;
  label: string;
  kind: string; // integer | enum | string
  current: string;
  values: string[]; // enum options; empty otherwise
  min: number | null;
  max: number | null;
}

// ScalingFormPayload mirrors gui/bindings_stack.go:ScalingFormPayload. resolved
// is false (with error set) when the stack's manifest has no scaling block.
export interface ScalingFormPayload {
  resolved: boolean;
  stack_name: string;
  env: string;
  targets: ScalingTarget[];
  error: string;
}

// AIStartResult mirrors gui/bindings_ai.go:AIStartResult. ok is false (with a
// setup hint in error) when AI is disabled or unconfigured.
export interface AIStartResult {
  ok: boolean;
  error: string;
}

// AIConsentEvent is the payload of the packwright:ai:consent Wails event — the
// write-tool consent request the chat panel renders as a modal. Mirrors the map
// emitted by gui/bindings_ai.go:consentModal.
export interface AIConsentEvent {
  tool: string;
  resource: string;
  reason: string;
  profile: string;
  region: string;
  account: string;
  blast_hint: string;
  args: string;
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
  SlashCommandForm(slash: string): Promise<FormPayload>;
  RunSlashCommand(slash: string, inputs: Record<string, string>): Promise<RunResult>;
  ListProjects(): Promise<Project[]>;
  ListStacks(project: string, env: string): Promise<StackRow[]>;
  StackUpdate(project: string, env: string, stack: string, confirm: boolean): Promise<StackActionResult>;
  ScalingForm(project: string, env: string, stack: string): Promise<ScalingFormPayload>;
  StackScale(
    project: string,
    env: string,
    stack: string,
    deltas: Record<string, string>,
    confirm: boolean,
  ): Promise<StackActionResult>;
  StackDelete(project: string, env: string, stack: string): Promise<StackActionResult>;
  AIEnabled(): Promise<boolean>;
  StartAISession(): Promise<AIStartResult>;
  SendAIMessage(text: string): Promise<void>;
  RespondAIConsent(decision: string): Promise<void>;
  CloseAISession(): Promise<void>;
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
  slashCommandForm: (slash: string) => bindings().SlashCommandForm(slash),
  runSlashCommand: (slash: string, inputs: Record<string, string>) =>
    bindings().RunSlashCommand(slash, inputs),
  listProjects: () => bindings().ListProjects(),
  listStacks: (project: string, env: string) => bindings().ListStacks(project, env),
  stackUpdate: (project: string, env: string, stack: string, confirm: boolean) =>
    bindings().StackUpdate(project, env, stack, confirm),
  scalingForm: (project: string, env: string, stack: string) =>
    bindings().ScalingForm(project, env, stack),
  stackScale: (
    project: string,
    env: string,
    stack: string,
    deltas: Record<string, string>,
    confirm: boolean,
  ) => bindings().StackScale(project, env, stack, deltas, confirm),
  stackDelete: (project: string, env: string, stack: string) =>
    bindings().StackDelete(project, env, stack),
  aiEnabled: () => bindings().AIEnabled(),
  startAISession: () => bindings().StartAISession(),
  sendAIMessage: (text: string) => bindings().SendAIMessage(text),
  respondAIConsent: (decision: string) => bindings().RespondAIConsent(decision),
  closeAISession: () => bindings().CloseAISession(),
};
