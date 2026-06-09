// Canonical typing for the Wails-injected `window.go.gui.App` surface.
//
// Multiple modules call into the same App object (lib/api.ts for palette /
// theme / AWS context, lib/profile-switcher/index.ts for the profile RPCs).
// Each used to declare its own `Window.go.gui.App` shape, which TypeScript
// merged into a conflict — declaration merging requires the property types
// to match exactly. This file is the single place that augments Window so
// both call sites see one consistent type.

import type { WailsBindings } from './api';
import type { ProfileBindings } from './profile-switcher';

declare global {
  interface Window {
    go?: { gui?: { App?: WailsBindings & ProfileBindings } };
  }
}

export {};
