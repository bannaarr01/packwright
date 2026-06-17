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

// The @fontsource-variable/* packages ship only CSS — their package "main" is
// index.css, with no type declarations. A side-effect `import
// '@fontsource-variable/geist'` (whose only job is to inject the @font-face
// rules) therefore has no types, which TypeScript 6 rejects with "Cannot find
// module or type declarations for side-effect import". This wildcard teaches
// svelte-check that the bare specifiers resolve, covering both geist faces and
// any future @fontsource-variable/* addition.
declare module '@fontsource-variable/*';
