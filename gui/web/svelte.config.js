import { vitePreprocess } from '@sveltejs/vite-plugin-svelte';

// Svelte config for Packwright. vitePreprocess handles TypeScript inside
// <script lang="ts"> blocks; everything else is the Svelte 5 default.
export default {
  preprocess: vitePreprocess(),
  compilerOptions: {
    runes: true,
  },
};
