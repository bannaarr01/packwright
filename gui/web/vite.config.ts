import { defineConfig } from 'vite';
import { svelte } from '@sveltejs/vite-plugin-svelte';

// Vite config for the Packwright GUI frontend.
//
// - Builds to ./dist, which is what gui/embed.go //go:embed-s into the binary.
// - emptyOutDir is true so a stale placeholder index.html is removed on build.
// - The Wails CLI proxies a dev server here when `wails dev` runs; in
//   production `wails build` invokes `npm run build` per wails.json.
export default defineConfig({
  plugins: [svelte()],
  build: {
    outDir: 'dist',
    emptyOutDir: true,
    target: 'es2022',
  },
});
