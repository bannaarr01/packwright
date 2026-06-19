<script lang="ts">
  import { onMount } from 'svelte';
  import { EventsOn } from '../../wailsjs/wailsjs/runtime/runtime';
  import { api, type AIConsentEvent } from '../api';

  // AIPanel is the GUI AI assistant (/ai), mirroring the TUI chat panel over
  // internal/ai/chat.Session via the gui/bindings_ai.go bridge.
  //
  // The session is built on mount (StartAISession); a failure surfaces the
  // engine's setup hint. Each turn streams as packwright:ai:* Wails events that
  // this component accumulates into the transcript. Write-tool consent
  // (ADR-0036) arrives as packwright:ai:consent and is rendered as a blocking
  // modal whose buttons answer via RespondAIConsent — deny is the default if the
  // user closes the panel mid-prompt (the Go bridge denies on teardown).

  interface Props {
    onClose: () => void;
  }

  const { onClose }: Props = $props();

  type Role = 'user' | 'assistant' | 'tool' | 'system';
  interface Msg {
    role: Role;
    text: string;
    live?: boolean;
  }

  let starting = $state(true);
  let startError = $state('');
  let ready = $state(false);
  let streaming = $state(false);
  let input = $state('');
  let messages = $state<Msg[]>([]);
  let pendingConsent = $state<AIConsentEvent | null>(null);
  let scroller: HTMLDivElement | undefined = $state();

  function scrollToEnd() {
    queueMicrotask(() => scroller?.scrollTo({ top: scroller.scrollHeight }));
  }

  function appendAssistant(text: string) {
    const last = messages[messages.length - 1];
    if (last && last.role === 'assistant' && last.live) {
      last.text += text;
      messages = messages;
    } else {
      messages = [...messages, { role: 'assistant', text, live: true }];
    }
    scrollToEnd();
  }

  function push(role: Role, text: string) {
    messages = [...messages, { role, text }];
    scrollToEnd();
  }

  // endTurn marks the streaming assistant bubble final and re-enables input.
  function endTurn() {
    const last = messages[messages.length - 1];
    if (last && last.live) {
      last.live = false;
      messages = messages;
    }
    streaming = false;
  }

  async function start() {
    const res = await api.startAISession();
    starting = false;
    if (!res.ok) {
      startError = res.error || 'AI is unavailable.';
      return;
    }
    ready = true;
  }

  function send() {
    const text = input.trim();
    if (!text || streaming || !ready) return;
    push('user', text);
    input = '';
    streaming = true;
    void api.sendAIMessage(text);
  }

  function onInputKeydown(e: KeyboardEvent) {
    if (e.key === 'Enter' && !e.shiftKey) {
      e.preventDefault();
      send();
    }
  }

  function answerConsent(decision: 'approve_once' | 'approve_session' | 'deny') {
    pendingConsent = null;
    void api.respondAIConsent(decision);
  }

  function onKeydown(e: KeyboardEvent) {
    // Esc closes the panel — but not while a consent prompt is up, so a stray
    // Esc never silently denies; the user must choose on the modal.
    if (e.key === 'Escape' && !pendingConsent) {
      e.preventDefault();
      onClose();
    }
  }

  onMount(() => {
    const offs = [
      EventsOn('packwright:ai:text', (d: { text: string }) => appendAssistant(d?.text ?? '')),
      EventsOn('packwright:ai:tool', (d: { name: string; phase: string; is_error?: boolean }) => {
        if (d?.phase === 'start') push('tool', `→ ${d.name}…`);
        else push('tool', `${d?.is_error ? '✗' : '✓'} ${d?.name}`);
      }),
      EventsOn('packwright:ai:consent', (d: AIConsentEvent) => {
        pendingConsent = d;
      }),
      EventsOn('packwright:ai:cap', (d: { message: string }) => {
        push('system', d?.message ?? 'Budget cap reached.');
        endTurn();
      }),
      EventsOn('packwright:ai:error', (d: { error: string }) => {
        push('system', `Error: ${d?.error ?? 'unknown'}`);
        endTurn();
      }),
      EventsOn('packwright:ai:done', () => endTurn()),
    ];
    void start();
    return () => {
      for (const off of offs) off?.();
      void api.closeAISession();
    };
  });
</script>

<svelte:window onkeydown={onKeydown} />

<button
  type="button"
  class="absolute inset-0 bg-black/40 cursor-default"
  aria-label="Close assistant"
  onclick={onClose}
></button>

<div
  class="absolute left-1/2 top-16 -translate-x-1/2 w-[min(760px,92%)] h-[min(72vh,640px)] rounded-lg shadow-2xl
         bg-light-bg dark:bg-dark-bg
         border border-light-border dark:border-dark-border
         text-light-fg dark:text-dark-fg overflow-hidden flex flex-col"
>
  <div
    class="px-4 py-3 border-b border-light-border dark:border-dark-border flex items-center gap-2 shrink-0"
  >
    <span class="w-1.5 h-1.5 rounded-full bg-dark-accent shrink-0"></span>
    <span class="font-mono text-sm">/ai</span>
    <span class="opacity-60 text-sm">assistant</span>
    {#if streaming}<span class="ml-auto text-xs opacity-50">thinking…</span>{/if}
  </div>

  <div bind:this={scroller} class="flex-1 overflow-y-auto p-4 space-y-3">
    {#if starting}
      <p class="opacity-60 text-sm">Starting the assistant…</p>
    {:else if startError}
      <div class="text-sm">
        <p style="color:#e5484d" class="mb-2">{startError}</p>
        <p class="opacity-70">
          Configure a provider and key with
          <code class="px-1 rounded bg-dark-border/40 font-mono">packwright ai setup</code>
          in your terminal, then reopen the assistant.
        </p>
      </div>
    {:else}
      {#if messages.length === 0}
        <p class="opacity-50 text-sm">
          Ask about an AWS failure, a stack, or what to deploy. The assistant reads by default and
          asks before any write.
        </p>
      {/if}
      {#each messages as m, i (i)}
        {#if m.role === 'user'}
          <div class="flex justify-end">
            <div
              class="max-w-[80%] px-3 py-2 rounded-lg text-sm
                     bg-light-selection_bg dark:bg-dark-selection_bg
                     text-light-selection_fg dark:text-dark-selection_fg whitespace-pre-wrap"
            >
              {m.text}
            </div>
          </div>
        {:else if m.role === 'assistant'}
          <div class="max-w-[88%] text-sm whitespace-pre-wrap leading-relaxed">{m.text}</div>
        {:else if m.role === 'tool'}
          <div class="text-xs font-mono opacity-60">{m.text}</div>
        {:else}
          <div class="text-xs" style="color:#f5a623">{m.text}</div>
        {/if}
      {/each}
    {/if}
  </div>

  {#if ready}
    <div class="border-t border-light-border dark:border-dark-border p-3 shrink-0">
      <div
        class="flex items-end gap-2 px-2 py-1.5 rounded-md
               bg-dark-border/25 border border-transparent focus-within:border-dark-accent_alt/50"
      >
        <textarea
          bind:value={input}
          onkeydown={onInputKeydown}
          rows="1"
          placeholder={streaming ? 'Waiting for the assistant…' : 'Message the assistant… (Enter to send)'}
          disabled={streaming}
          class="flex-1 bg-transparent outline-none resize-none text-sm max-h-32 py-1
                 placeholder:text-dark-fg/35 disabled:opacity-50"
        ></textarea>
        <button
          type="button"
          onclick={send}
          disabled={streaming || input.trim() === ''}
          class="px-3 py-1.5 rounded text-sm shrink-0
                 bg-light-selection_bg dark:bg-dark-selection_bg
                 text-light-selection_fg dark:text-dark-selection_fg
                 disabled:opacity-40"
        >
          Send
        </button>
      </div>
    </div>
  {/if}
</div>

{#if pendingConsent}
  <!-- Write-consent modal (ADR-0036). Rendered above the panel; the only way
       past it is an explicit choice. -->
  <div class="absolute inset-0 bg-black/50 grid place-items-center">
    <div
      class="w-[min(520px,90%)] rounded-lg shadow-2xl p-4
             bg-light-bg dark:bg-dark-bg border text-light-fg dark:text-dark-fg"
      style="border-color:#e5484d66"
    >
      <p class="text-sm font-medium mb-2" style="color:#e5484d">
        The assistant wants to run a write tool
      </p>
      <div class="text-sm space-y-1 mb-3 font-mono">
        <div><span class="opacity-50">tool</span> {pendingConsent.tool}</div>
        {#if pendingConsent.resource}<div><span class="opacity-50">target</span> {pendingConsent.resource}</div>{/if}
        {#if pendingConsent.account || pendingConsent.region}
          <div><span class="opacity-50">where</span> {pendingConsent.profile} · {pendingConsent.account} · {pendingConsent.region}</div>
        {/if}
        {#if pendingConsent.blast_hint}<div style="color:#f5a623">{pendingConsent.blast_hint}</div>{/if}
      </div>
      {#if pendingConsent.reason}
        <p class="text-sm opacity-80 mb-3">“{pendingConsent.reason}”</p>
      {/if}
      {#if pendingConsent.args}
        <pre class="text-xs font-mono whitespace-pre-wrap opacity-70 mb-3 max-h-32 overflow-y-auto
                    bg-dark-border/20 rounded p-2">{pendingConsent.args}</pre>
      {/if}
      <div class="flex gap-2 justify-end">
        <button
          type="button"
          onclick={() => answerConsent('deny')}
          class="px-3 py-1.5 rounded text-sm border border-light-border dark:border-dark-border"
        >
          Deny
        </button>
        <button
          type="button"
          onclick={() => answerConsent('approve_once')}
          class="px-3 py-1.5 rounded text-sm text-white"
          style="background:#e5484d"
        >
          Approve once
        </button>
        <button
          type="button"
          onclick={() => answerConsent('approve_session')}
          class="px-3 py-1.5 rounded text-sm border"
          style="border-color:#e5484d99;color:#e5484d"
        >
          Approve session
        </button>
      </div>
    </div>
  </div>
{/if}
