// Typed wrapper around the gui/bindings_profile.go RPC surface plus the
// Switcher.svelte barrel. The shapes here must mirror the Go-side structs in
// gui/bindings_profile.go — there is exactly one place to look in each
// language. The naming follows lib/api.ts: a `switcherApi` namespace so call
// sites read like `await switcherApi.list()`.

import { default as Switcher } from './Switcher.svelte';
import { default as RegionSwitcher } from './RegionSwitcher.svelte';

export { Switcher, RegionSwitcher };

export interface ProfileEntry {
  name: string;
  region: string;
  active: boolean;
}

export interface IdentityPayload {
  profile: string;
  region: string;
  account: string;
  arn: string;
  userId: string;
}

export interface SwitchResult {
  ok: boolean;
  identity?: IdentityPayload;
  error?: string;
  suggested?: string[];
}

// Exported so wails-app.d.ts can intersect it with WailsBindings into the
// single canonical Window.go.gui.App typing. The previous local
// `declare global` here conflicted with lib/api.ts's parallel declaration
// — TypeScript merges interfaces but requires matching property types.
export interface ProfileBindings {
  ListProfiles(): Promise<ProfileEntry[]>;
  SwitchProfile(profile: string, region: string): Promise<SwitchResult>;
  VerifyCurrent(): Promise<SwitchResult>;
  ListRegions(): Promise<string[]>;
  SwitchRegion(region: string): Promise<SwitchResult>;
}

// bindings reaches the App methods at runtime. Mirrors lib/api.ts's pattern so
// the panel fails loudly when invoked outside the Wails runtime.
function bindings(): ProfileBindings {
  const api = window.go?.gui?.App;
  if (!api) {
    throw new Error(
      'Wails bindings not ready — window.go.gui.App is undefined. ' +
        'Are you running outside the Wails runtime?',
    );
  }
  return api;
}

export const switcherApi = {
  list: (): Promise<ProfileEntry[]> => bindings().ListProfiles(),
  switch: (profile: string, region: string): Promise<SwitchResult> =>
    bindings().SwitchProfile(profile, region),
  verify: (): Promise<SwitchResult> => bindings().VerifyCurrent(),
};

// regionApi is the region counterpart of switcherApi, backed by
// gui/bindings_region.go. list() returns the account's enabled regions (with a
// static fallback); switch() changes only the region, holding the profile fixed.
export const regionApi = {
  list: (): Promise<string[]> => bindings().ListRegions(),
  switch: (region: string): Promise<SwitchResult> =>
    bindings().SwitchRegion(region),
};

// PROFILE_SWITCHED_EVENT mirrors gui/bindings_profile.go's ProfileSwitchedEvent
// constant. Header subscribers wire it through Wails' runtime.EventsOn (loaded
// from `../../runtime/runtime.js` in production builds; consumers can also
// poll the result returned by SwitchProfile directly).
export const PROFILE_SWITCHED_EVENT = 'packwright:profile-switched';
