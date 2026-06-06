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
        sans: ['ui-sans-serif', 'system-ui', '-apple-system', 'sans-serif'],
        mono: ['ui-monospace', 'SFMono-Regular', 'Menlo', 'monospace'],
      },
    },
  },
  plugins: [],
};

export default config;
