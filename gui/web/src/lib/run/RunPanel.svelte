<script lang="ts">
  import { onMount } from 'svelte';
  import { api, type FormField, type RunResult } from '../api';

  // RunPanel drives a manifest command end-to-end in the GUI: it fetches the
  // command's form schema (SlashCommandForm), collects inputs, then dispatches
  // it through the action engine (RunSlashCommand) and renders the collected
  // output. It mirrors the TUI's form → run flow. Live token-by-token
  // streaming is a follow-up; today the RPC resolves when the run finishes and
  // the panel shows a "running…" state until then.

  interface Props {
    slash: string;
    title: string;
    onClose: () => void;
  }

  const { slash, title, onClose }: Props = $props();

  type Phase = 'loading' | 'form' | 'running' | 'done' | 'error';

  let phase = $state<Phase>('loading');
  let fields = $state<FormField[]>([]);
  let values = $state<Record<string, string>>({});
  let result = $state<RunResult | null>(null);
  let errorMsg = $state('');

  onMount(async () => {
    try {
      const form = await api.slashCommandForm(slash);
      if (!form.resolved) {
        errorMsg = `No command found for ${slash}.`;
        phase = 'error';
        return;
      }
      fields = form.fields ?? [];
      const seed: Record<string, string> = {};
      for (const f of fields) seed[f.id] = '';
      values = seed;
      // A command with no inputs can run immediately.
      phase = fields.length === 0 ? 'form' : 'form';
    } catch (err) {
      errorMsg = err instanceof Error ? err.message : String(err);
      phase = 'error';
    }
  });

  async function run() {
    phase = 'running';
    try {
      result = await api.runSlashCommand(slash, values);
      phase = 'done';
    } catch (err) {
      errorMsg = err instanceof Error ? err.message : String(err);
      phase = 'error';
    }
  }

  function onKeydown(e: KeyboardEvent) {
    if (e.key === 'Escape') {
      e.preventDefault();
      onClose();
    }
  }
</script>

<svelte:window onkeydown={onKeydown} />

<!-- Backdrop closes the panel. -->
<button
  type="button"
  class="absolute inset-0 bg-black/40 cursor-default"
  aria-label="Close run panel"
  onclick={onClose}
></button>

<div
  class="absolute left-1/2 top-24 -translate-x-1/2 w-[min(680px,90%)] rounded-lg shadow-2xl
         bg-light-bg dark:bg-dark-bg
         border border-light-border dark:border-dark-border
         text-light-fg dark:text-dark-fg overflow-hidden"
>
  <div
    class="px-4 py-3 border-b border-light-border dark:border-dark-border flex items-baseline gap-3"
  >
    <span class="font-mono text-sm">{slash}</span>
    <span class="opacity-70 text-sm">{title}</span>
  </div>

  <div class="p-4 max-h-[60vh] overflow-y-auto">
    {#if phase === 'loading'}
      <p class="opacity-60 text-sm">Loading…</p>
    {:else if phase === 'error'}
      <p class="text-sm" style="color:#e5484d">{errorMsg}</p>
    {:else if phase === 'form'}
      {#each fields as f (f.id)}
        <label class="block mb-3">
          <span class="block text-sm mb-1">
            {f.label || f.id}{#if f.required}<span class="opacity-60"> *</span>{/if}
          </span>
          <input
            bind:value={values[f.id]}
            placeholder={f.placeholder}
            class="w-full px-3 py-2 rounded bg-transparent outline-none
                   border border-light-border dark:border-dark-border"
            autocomplete="off"
            spellcheck="false"
          />
        </label>
      {/each}
      <button
        type="button"
        onclick={run}
        class="mt-2 px-4 py-2 rounded text-sm
               bg-light-selection_bg dark:bg-dark-selection_bg
               text-light-selection_fg dark:text-dark-selection_fg"
      >
        Run {slash}
      </button>
    {:else if phase === 'running'}
      <p class="opacity-60 text-sm">Running {slash}…</p>
    {:else if phase === 'done' && result}
      <p class="text-sm mb-2">{result.ok ? '✓ completed' : '✗ failed'}</p>
      {#if result.error}
        <p class="text-sm mb-2" style="color:#e5484d">{result.error}</p>
      {/if}
      {#if result.output && result.output.length > 0}
        <pre class="text-xs font-mono whitespace-pre-wrap opacity-80">{result.output.join('\n')}</pre>
      {/if}
      <button
        type="button"
        onclick={onClose}
        class="mt-3 px-4 py-2 rounded text-sm
               border border-light-border dark:border-dark-border"
      >
        Close
      </button>
    {/if}
  </div>
</div>
