<script>
  import {onDestroy, onMount} from 'svelte'
  export let confirmText = ''
  export let inputValue = ''
  export let clearing = false
  export let onCancel = () => {}
  // onConfirm receives {deleteProfileData, deleteAPIKey}. Both are false unless the user turns them on.
  export let onConfirm = () => {}

  // Both toggles start off. App mounts this modal only while it is open, so each open starts from these defaults.
  let deleteProfileData = false
  let deleteAPIKey = false
  let copied = false
  let dialog = null
  // The element that had focus when the modal opened. Focus goes back to it on close, if it is still in the page.
  let returnFocus = null

  $: canConfirm = inputValue === confirmText && !clearing
  $: anySwitchOn = deleteProfileData || deleteAPIKey
  $: confirmLabel = confirmButtonLabel(clearing, deleteProfileData, deleteAPIKey)
  $: outcome = outcomeText(deleteProfileData, deleteAPIKey)

  onMount(() => {
    returnFocus = document.activeElement
    dialog?.focus()
  })

  onDestroy(() => {
    if (returnFocus instanceof HTMLElement && returnFocus.isConnected) returnFocus.focus()
  })

  function confirmButtonLabel(busy, profileData, apiKey) {
    if (busy) return 'Removing…'
    if (profileData && apiKey) return 'Remove profile, data and key'
    if (profileData) return 'Remove profile and data'
    if (apiKey) return 'Remove profile and key'
    return 'Remove profile'
  }

  function outcomeText(profileData, apiKey) {
    if (profileData && apiKey) return "The profile, its data and the saved key are deleted. This can't be undone."
    if (profileData) return "The profile and its data are deleted. This can't be undone. The API key is kept."
    if (apiKey) return "The saved key is deleted; profile data is kept. You'll need to enter a key to sync again."
    return 'Nothing is deleted. The profile is disconnected; connecting the same Steam profile again restores everything.'
  }

  function confirmClear() {
    if (!canConfirm) return
    onConfirm({deleteProfileData, deleteAPIKey})
  }

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
  <div bind:this={dialog} class="confirm-modal" role="dialog" aria-modal="true" aria-labelledby="clear-data-title" tabindex="-1">
    <div class="confirm-modal-head">
      <p class="eyebrow">Danger zone</p>
      <h2 id="clear-data-title">Remove active profile?</h2>
    </div>

    <p class="muted">This removes the active profile from the app. Nothing else is deleted unless you turn it on.</p>

    <div class="setting-row">
      <input
        id="clear-delete-profile-data"
        type="checkbox"
        role="switch"
        bind:checked={deleteProfileData}
        disabled={clearing}
        aria-describedby="clear-delete-profile-data-hint"
      />
      <div>
        <label for="clear-delete-profile-data">Delete profile data</label>
        <p id="clear-delete-profile-data-hint" class="muted">Tracked games, achievements, sync history, settings and cached images.</p>
      </div>
    </div>

    <div class="setting-row">
      <input
        id="clear-delete-api-key"
        type="checkbox"
        role="switch"
        bind:checked={deleteAPIKey}
        disabled={clearing}
        aria-describedby="clear-delete-api-key-hint"
      />
      <div>
        <label for="clear-delete-api-key">Delete API key</label>
        <p id="clear-delete-api-key-hint" class="muted">Removes the saved key from the system keyring (Secret Service). The app can't sync until you enter a key again.</p>
      </div>
    </div>

    <p class="confirm-outcome" class:destructive={anySwitchOn} aria-live="polite">{outcome}</p>

    <div class="confirm-token-block">
      <span>Type this exact value to continue:</span>
      <div class="confirm-token-row">
        <code id="clear-data-token" class="confirm-token">{confirmText}</code>
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
      aria-describedby="clear-data-token"
      on:keydown={(event) => event.key === 'Enter' && confirmClear()}
    />

    <div class="confirm-actions">
      <button class="secondary" on:click={onCancel} disabled={clearing}>Cancel</button>
      <button class="danger" on:click={confirmClear} disabled={!canConfirm}>{confirmLabel}</button>
    </div>
  </div>
</div>
