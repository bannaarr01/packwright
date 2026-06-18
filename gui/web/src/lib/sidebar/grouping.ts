// Sidebar grouping mode store.
//
// PR-10 adds a second grouping mode (Projects) alongside the existing
// pack/scope grouping. The user's choice is persisted in localStorage under
// `packwright.sidebar.grouping` so the window opens the way they left it.
// "projects" is the default for a fresh install per ADR-0045.

import { writable } from 'svelte/store';

export type Grouping = 'projects' | 'by-pack';

const STORAGE_KEY = 'packwright.sidebar.grouping';

function readInitial(): Grouping {
  // localStorage can throw on first read in restricted contexts (Safari
  // private mode, sandboxed iframes). Default to 'projects' rather than
  // surface the error — the choice will simply not survive a refresh.
  try {
    const v = localStorage.getItem(STORAGE_KEY);
    return v === 'by-pack' ? 'by-pack' : 'projects';
  } catch {
    return 'projects';
  }
}

export const grouping = writable<Grouping>(readInitial());

grouping.subscribe((value) => {
  try {
    localStorage.setItem(STORAGE_KEY, value);
  } catch {
    // Best-effort. Subsequent launches will see the default again.
  }
});
