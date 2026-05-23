<script>
  export let confirmText = ''
  export let inputValue = ''
  export let clearing = false
  export let onCancel = () => {}
  export let onConfirm = () => {}

  let copied = false

  $: canConfirm = inputValue === confirmText && !clearing

  async function copyConfirmText() {
    try {
      await navigator.clipboard.writeText(confirmText)
      copied = true
      setTimeout(() => (copied = false), 1600)
    } catch {
      copied = false
    }
  }
</script>

<div class="modal-backdrop" role="presentation">
  <section class="confirm-modal" role="dialog" aria-modal="true" aria-labelledby="clear-data-title">
    <div class="confirm-modal-head">
      <p class="eyebrow">Danger zone</p>
      <h2 id="clear-data-title">Clear active user data?</h2>
    </div>

    <p class="muted">
      This deletes this active profile, tracked games, achievement data, sync history, manual flags, and cached game images.
      Secret Service API key and logs stay.
    </p>

    <div class="confirm-token-block">
      <span>Type this exact value to continue:</span>
      <div class="confirm-token-row">
        <code class="confirm-token">{confirmText}</code>
        <button class="confirm-token-copy" type="button" on:click={copyConfirmText} aria-label={copied ? 'Copied confirmation value' : 'Copy confirmation value'} title={copied ? 'Copied' : 'Copy'}>
          {#if copied}
            <svg viewBox="0 0 24 24" aria-hidden="true"><path d="M20.3 5.7a1 1 0 0 1 0 1.4l-10 10a1 1 0 0 1-1.4 0l-5-5a1 1 0 1 1 1.4-1.4l4.3 4.29 9.3-9.29a1 1 0 0 1 1.4 0Z" /></svg>
          {:else}
            <svg viewBox="0 0 24 24" aria-hidden="true"><path d="M8 7a3 3 0 0 1 3-3h6a3 3 0 0 1 3 3v6a3 3 0 0 1-3 3h-1v1a3 3 0 0 1-3 3H7a3 3 0 0 1-3-3v-6a3 3 0 0 1 3-3h1V7Zm2 1h3a3 3 0 0 1 3 3v3h1a1 1 0 0 0 1-1V7a1 1 0 0 0-1-1h-6a1 1 0 0 0-1 1v1Zm-3 2a1 1 0 0 0-1 1v6a1 1 0 0 0 1 1h6a1 1 0 0 0 1-1v-6a1 1 0 0 0-1-1H7Z" /></svg>
          {/if}
        </button>
      </div>
    </div>

    <label class="sr-only" for="clear-data-confirm-input">Type this exact value to continue</label>

    <input
      id="clear-data-confirm-input"
      class="confirm-input"
      bind:value={inputValue}
      autocomplete="off"
      spellcheck="false"
      disabled={clearing}
      on:keydown={(event) => event.key === 'Enter' && canConfirm && onConfirm()}
    />

    <div class="confirm-actions">
      <button class="secondary" on:click={onCancel} disabled={clearing}>Cancel</button>
      <button class="danger" on:click={onConfirm} disabled={!canConfirm}>{clearing ? 'Clearing…' : 'Clear data'}</button>
    </div>
  </section>
</div>
