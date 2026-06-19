<script lang="ts">
  import {
    api,
    type DiffPayload,
    type ScalingFormPayload,
    type StackActionResult,
  } from '../api';
  import type { StackTarget } from './stackPanel';

  // StackActionPanel drives the MVP-7 stack actions for one stack in the GUI:
  // update (in-place change set), scale (parameter-only change), and delete
  // (a notice — the destructive path lives in the audit deletion tray). It
  // mirrors the TUI sidebar's u/s/d flow and the gui/bindings_stack.go backend.
  //
  // Replacement / env-guard confirmation uses the backend's two-call pattern:
  // the first call (confirm=false) previews and, for a destructive change,
  // returns outcome="needs_confirm" with the diff; the user reviews it and the
  // panel re-calls with confirm=true. The default never replaces a resource
  // without that explicit second action.

  interface Props {
    target: StackTarget;
    onClose: () => void;
  }

  const { target, onClose }: Props = $props();

  type Phase = 'menu' | 'loading' | 'scale-form' | 'result';
  type Action = 'update' | 'scale' | 'delete';

  let phase = $state<Phase>('menu');
  let action = $state<Action>('update');
  let busyLabel = $state('Working…');
  let result = $state<StackActionResult | null>(null);
  let scaleForm = $state<ScalingFormPayload | null>(null);
  let scaleValues = $state<Record<string, string>>({});

  async function runUpdate(confirm: boolean) {
    action = 'update';
    busyLabel = confirm ? 'Applying update…' : 'Computing change set…';
    phase = 'loading';
    result = await api.stackUpdate(target.project, target.env, target.stack, confirm);
    phase = 'result';
  }

  async function openScale() {
    busyLabel = 'Loading scaling form…';
    phase = 'loading';
    const form = await api.scalingForm(target.project, target.env, target.stack);
    if (!form.resolved) {
      action = 'scale';
      result = {
        ok: false,
        outcome: 'error',
        notice: '',
        needs_confirm: false,
        confirm_reason: '',
        output: [],
        error: form.error || 'This stack has no scaling block.',
      };
      phase = 'result';
      return;
    }
    scaleForm = form;
    const seed: Record<string, string> = {};
    for (const t of form.targets) seed[t.param] = t.current;
    scaleValues = seed;
    phase = 'scale-form';
  }

  async function applyScale(confirm: boolean) {
    action = 'scale';
    busyLabel = confirm ? 'Applying scale…' : 'Computing change set…';
    phase = 'loading';
    result = await api.stackScale(target.project, target.env, target.stack, scaleValues, confirm);
    phase = 'result';
  }

  async function runDelete() {
    action = 'delete';
    busyLabel = 'Loading…';
    phase = 'loading';
    result = await api.stackDelete(target.project, target.env, target.stack);
    phase = 'result';
  }

  // confirmAgain re-runs the action that asked for confirmation, this time with
  // confirm=true so the backend's consent gate approves.
  function confirmAgain() {
    if (action === 'update') void runUpdate(true);
    else if (action === 'scale') void applyScale(true);
  }

  function backToMenu() {
    phase = 'menu';
    result = null;
    scaleForm = null;
  }

  function onKeydown(e: KeyboardEvent) {
    if (e.key === 'Escape') {
      e.preventDefault();
      onClose();
    }
  }

  // Flatten a diff into display buckets with their accent class. Empty buckets
  // are dropped so the modal only shows what actually changes.
  function diffBuckets(diff: DiffPayload) {
    return [
      { key: 'replace', label: 'Replace', rows: diff.replaces, cls: 'text-[#e5484d]' },
      { key: 'delete', label: 'Delete', rows: diff.deletes, cls: 'text-[#e5484d]' },
      { key: 'add', label: 'Add', rows: diff.adds, cls: 'text-[#46a758]' },
      { key: 'modify', label: 'Modify', rows: diff.modifies, cls: 'text-[#f5a623]' },
    ].filter((b) => b.rows.length > 0);
  }
</script>

<svelte:window onkeydown={onKeydown} />

<button
  type="button"
  class="absolute inset-0 bg-black/40 cursor-default"
  aria-label="Close stack panel"
  onclick={onClose}
></button>

<div
  class="absolute left-1/2 top-20 -translate-x-1/2 w-[min(720px,92%)] rounded-lg shadow-2xl
         bg-light-bg dark:bg-dark-bg
         border border-light-border dark:border-dark-border
         text-light-fg dark:text-dark-fg overflow-hidden"
>
  <div
    class="px-4 py-3 border-b border-light-border dark:border-dark-border flex items-baseline gap-3"
  >
    <span class="font-mono text-sm">{target.slash}</span>
    <span class="opacity-70 text-sm truncate">{target.stack}</span>
    <span class="ml-auto text-[11px] opacity-50 font-mono">{target.project}/{target.env}</span>
  </div>

  <div class="p-4 max-h-[64vh] overflow-y-auto">
    {#if phase === 'menu'}
      <p class="text-sm opacity-70 mb-3">Choose an action for this stack.</p>
      <div class="space-y-2">
        <button
          type="button"
          onclick={() => runUpdate(false)}
          class="w-full text-left px-3 py-2 rounded border border-light-border dark:border-dark-border
                 hover:bg-light-selection_bg dark:hover:bg-dark-selection_bg transition"
        >
          <span class="text-sm font-medium">Update</span>
          <span class="block text-xs opacity-60">Apply the manifest's current template in place (change-set diff first).</span>
        </button>
        <button
          type="button"
          onclick={openScale}
          class="w-full text-left px-3 py-2 rounded border border-light-border dark:border-dark-border
                 hover:bg-light-selection_bg dark:hover:bg-dark-selection_bg transition"
        >
          <span class="text-sm font-medium">Scale</span>
          <span class="block text-xs opacity-60">Adjust scaling parameters (env guards enforced).</span>
        </button>
        <button
          type="button"
          onclick={runDelete}
          class="w-full text-left px-3 py-2 rounded border border-light-border dark:border-dark-border
                 hover:bg-light-selection_bg dark:hover:bg-dark-selection_bg transition"
        >
          <span class="text-sm font-medium">Delete</span>
          <span class="block text-xs opacity-60">Where to safely delete this stack.</span>
        </button>
      </div>
    {:else if phase === 'loading'}
      <p class="opacity-60 text-sm">{busyLabel}</p>
    {:else if phase === 'scale-form' && scaleForm}
      <p class="text-sm opacity-70 mb-3">
        Scaling {scaleForm.stack_name} · env {scaleForm.env}
      </p>
      {#each scaleForm.targets as t (t.param)}
        <label class="block mb-3">
          <span class="block text-sm mb-1">
            {t.label}
            <span class="opacity-50 text-xs">({t.kind}{#if t.min !== null} · min {t.min}{/if}{#if t.max !== null} · max {t.max}{/if})</span>
          </span>
          {#if t.values.length > 0}
            <select
              bind:value={scaleValues[t.param]}
              class="w-full px-3 py-2 rounded bg-transparent outline-none
                     border border-light-border dark:border-dark-border"
            >
              {#each t.values as v (v)}
                <option value={v}>{v}</option>
              {/each}
            </select>
          {:else}
            <input
              bind:value={scaleValues[t.param]}
              class="w-full px-3 py-2 rounded bg-transparent outline-none
                     border border-light-border dark:border-dark-border"
              autocomplete="off"
              spellcheck="false"
            />
          {/if}
        </label>
      {/each}
      <div class="flex gap-2 mt-2">
        <button
          type="button"
          onclick={() => applyScale(false)}
          class="px-4 py-2 rounded text-sm
                 bg-light-selection_bg dark:bg-dark-selection_bg
                 text-light-selection_fg dark:text-dark-selection_fg"
        >
          Apply scale
        </button>
        <button
          type="button"
          onclick={backToMenu}
          class="px-4 py-2 rounded text-sm border border-light-border dark:border-dark-border"
        >
          Back
        </button>
      </div>
    {:else if phase === 'result' && result}
      {#if result.outcome === 'error'}
        <p class="text-sm" style="color:#e5484d">{result.error}</p>
      {:else if result.outcome === 'needs_confirm'}
        <div class="mb-3 px-3 py-2 rounded border" style="border-color:#e5484d55;background:#e5484d11">
          <p class="text-sm font-medium" style="color:#e5484d">Confirmation required</p>
          <p class="text-sm mt-1">{result.confirm_reason}</p>
        </div>
      {:else}
        <p class="text-sm mb-2">
          {#if result.outcome === 'executed'}✓ {result.notice || 'Change set executed.'}
          {:else if result.outcome === 'no_changes'}• {result.notice || 'No changes to apply.'}
          {:else if result.outcome === 'notice'}{result.notice}
          {:else}{result.notice}{/if}
        </p>
      {/if}

      {#if result.diff && !result.diff.no_changes}
        {@const buckets = diffBuckets(result.diff)}
        {#if buckets.length > 0}
          <div class="mt-2 mb-3 text-xs font-mono space-y-1">
            {#each buckets as b (b.key)}
              {#each b.rows as r (r.logical_id)}
                <div class="flex gap-2">
                  <span class={b.cls + ' w-16 shrink-0'}>{b.label}</span>
                  <span class="truncate">
                    {r.logical_id}
                    <span class="opacity-50">{r.resource_type}</span>
                    {#if r.iam}<span style="color:#f5a623"> [IAM]</span>{/if}
                  </span>
                </div>
              {/each}
            {/each}
          </div>
        {/if}
      {/if}

      {#if result.output && result.output.length > 0}
        <pre class="text-xs font-mono whitespace-pre-wrap opacity-80 mt-2">{result.output.join('\n')}</pre>
      {/if}

      <div class="flex gap-2 mt-3">
        {#if result.needs_confirm}
          <button
            type="button"
            onclick={confirmAgain}
            class="px-4 py-2 rounded text-sm text-white"
            style="background:#e5484d"
          >
            Confirm &amp; apply
          </button>
          <button
            type="button"
            onclick={backToMenu}
            class="px-4 py-2 rounded text-sm border border-light-border dark:border-dark-border"
          >
            Cancel
          </button>
        {:else}
          <button
            type="button"
            onclick={backToMenu}
            class="px-4 py-2 rounded text-sm border border-light-border dark:border-dark-border"
          >
            Back
          </button>
          <button
            type="button"
            onclick={onClose}
            class="px-4 py-2 rounded text-sm border border-light-border dark:border-dark-border"
          >
            Close
          </button>
        {/if}
      </div>
    {/if}
  </div>
</div>
