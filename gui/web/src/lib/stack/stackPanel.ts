import { writable } from 'svelte/store';

// StackTarget identifies the stack the user opened from the sidebar. The
// (project, env) pair is the record-store coordinate the Go bindings need; slash
// + stack are shown in the panel header.
export interface StackTarget {
  project: string;
  env: string;
  stack: string;
  slash: string;
}

// stackPanel holds the stack the user opened, or null when no panel is open.
// StackRow sets it (ProjectGroup gives it the project/env/stack it needs);
// App.svelte renders StackActionPanel whenever it is non-null. A store avoids
// threading an open-stack callback down five component levels (App → Sidebar →
// ProjectsView → ProjectGroup → StackRow), mirroring how grouping.ts keeps the
// sidebar's view mode.
export const stackPanel = writable<StackTarget | null>(null);
