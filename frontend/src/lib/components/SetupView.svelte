<script>
  import {tick} from 'svelte'
  import ApiKeyForm from './ApiKeyForm.svelte'
  export let mode = 'profile'
  export let state
  export let error = ''
  export let profileInput = ''
  export let preview = null
  export let validating = false
  export let saving = false
  export let savingKey = false
  export let onRetry = () => {}
  export let onValidate = () => {}
  export let onSave = () => {}
  export let onSaveKey = () => {}
  export let onProfileInput = () => {}

  let showKeyForm = false
  let toggleButton = null

  // A failed save rejects through here, so the form stays open and shows the error; it closes only on success, and
  // focus goes back to the toggle that opened it.
  async function saveKey(key) {
    await onSaveKey(key)
    showKeyForm = false
    await tick()
    toggleButton?.focus()
  }
</script>

{#if mode === 'startup-error'}
  <main class="setup-screen setup-error"><section class="setup-panel"><p class="eyebrow">Steam Achievement Tracker</p><h1>Startup failed</h1><p class="muted">{state.initError}</p><p class="muted">Check {state.logFile || 'the log file'}.</p>{#if error}<p class="error-text">{error}</p>{/if}</section></main>
{:else if mode === 'missing-key'}
  <main class="setup-screen"><section class="setup-panel"><p class="eyebrow">Steam Achievement Tracker</p><h1>Steam Web API key missing</h1>{#if state?.apiKeyError}<p class="error-text">Secret Service error: {state.apiKeyError}</p>{/if}<p class="muted">Paste your Steam Web API key. It is stored only in your Secret Service keyring.</p><div class="api-key-setup"><ApiKeyForm saving={savingKey} autofocus onSave={onSaveKey} /></div>{#if error}<p class="error-text">{error}</p>{/if}<div class="button-row"><button class="secondary" on:click={onRetry} disabled={savingKey}>Retry key lookup</button></div></section></main>
{:else}
  <main class="setup-screen"><section class="setup-panel"><p class="eyebrow">Steam Achievement Tracker</p><h1>Connect your profile</h1><p class="muted">Paste a SteamID64, profile URL, or vanity name. Profile game details must be public.</p><div class="input-row"><input value={profileInput} placeholder="SteamID64, https://steamcommunity.com/id/name, or vanity" on:input={(e) => onProfileInput(e.currentTarget.value)} on:keydown={(e) => e.key === 'Enter' && onValidate()} /><button class="primary" on:click={onValidate} disabled={validating || savingKey}>{validating ? 'Checking…' : 'Validate'}</button></div>{#if error}<p class="error-text">{error}</p>{/if}{#if preview}<section class="confirm-card">{#if preview.avatarUrl}<img src={preview.avatarUrl} alt="" />{:else}<div class="avatar-fallback">{(preview.displayName || preview.steamId64 || 'S').slice(0, 1).toUpperCase()}</div>{/if}<div class="confirm-copy"><p class="eyebrow">Profile found</p><h2>{preview.displayName}</h2><p class="muted">{preview.steamId64}</p></div><button class="primary confirm-action" on:click={onSave} disabled={saving || savingKey}>{saving ? 'Saving…' : 'Save and sync'}</button></section>{/if}<div class="api-key-setup"><div class="button-row"><button bind:this={toggleButton} class="secondary" disabled={savingKey} aria-expanded={showKeyForm} on:click={() => (showKeyForm = !showKeyForm)}>Replace key</button></div>{#if showKeyForm}<ApiKeyForm saving={savingKey} disabled={validating || saving} autofocus showLabel onSave={saveKey} />{/if}</div></section></main>
{/if}
