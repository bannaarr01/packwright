import type { Config } from 'tailwindcss';
import dark from '../../internal/theme/tokens/dark.json';
import light from '../../internal/theme/tokens/light.json';

// Tailwind config for the Packwright GUI.
//
// The colour palette is sourced directly from internal/theme/tokens/*.json so
// the GUI cannot drift from the TUI. The Go side reads the same files via
// //go:embed; this file imports them via Vite's JSON resolver.
//
// Dark mode is class-based: the App component flips <html class="dark"> based
// on the Theme() RPC result.
const config: Config = {
  content: ['./index.html', './src/**/*.{svelte,ts}'],
  darkMode: 'class',
  theme: {
    extend: {
      colors: {
        // Two named scales — `light` and `dark` — keyed by semantic token name.
        // Components reference them as e.g. `bg-light-bg dark:bg-dark-bg`.
        light,
        dark,
      },
      fontFamily: {
        // Geist Variable pairs cleanly with Geist Mono — refined dev-tool
        // typography, deliberately not Inter/Roboto. The system-font tail is
        // a safety net for the rare offline boot before the font loads.
        sans: ['"Geist Variable"', 'ui-sans-serif', 'system-ui', 'sans-serif'],
        mono: [
          '"Geist Mono Variable"',
          'ui-monospace',
          'SFMono-Regular',
          'Menlo',
          'monospace',
        ],
      },
      letterSpacing: {
        // Sidebar section labels — caps + tracked = nav chrome without
        // screaming for attention.
        micro: '0.14em',
      },
    },
  },
  plugins: [],
};

export default config;
