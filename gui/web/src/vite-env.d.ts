/// <reference types="svelte" />
/// <reference types="vite/client" />

// Vite resolves SVG imports to the asset URL at build time. The runtime
// behaviour is already correct — this file only teaches TypeScript that
// `import url from './foo.svg'` yields a string so svelte-check stops
// failing the pre-push gate.
declare module '*.svg' {
  const url: string;
  export default url;
}
