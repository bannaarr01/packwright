import './styles/global.css';
import { mount } from 'svelte';
import App from './App.svelte';

// Mount the root component into #app. Svelte 5's `mount` is the idiomatic
// replacement for `new App({ target })` from Svelte 4 — components are
// instances of functions, not classes.
const target = document.getElementById('app');
if (!target) {
  throw new Error('Packwright GUI: #app mount point missing from index.html');
}

mount(App, { target });
