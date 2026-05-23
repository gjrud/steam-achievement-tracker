<script>
  export let mode = 'profile'
  export let state
  export let error = ''
  export let profileInput = ''
  export let preview = null
  export let validating = false
  export let saving = false
  export let onRetry = () => {}
  export let onValidate = () => {}
  export let onSave = () => {}
  export let onProfileInput = () => {}
</script>

{#if mode === 'startup-error'}
  <main class="setup-screen setup-error"><section class="setup-panel"><p class="eyebrow">Steam Achievement Tracker</p><h1>Startup failed</h1><p class="muted">{state.initError}</p><p class="muted">Check {state.logFile || 'the log file'}.</p>{#if error}<p class="error-text">{error}</p>{/if}</section></main>
{:else if mode === 'missing-key'}
  <main class="setup-screen"><section class="setup-panel"><p class="eyebrow">Steam Achievement Tracker</p><h1>Steam Web API key missing</h1><p class="muted">Secret Service only. No env vars. No local fallback.</p>{#if state?.apiKeyError}<p class="error-text">Secret Service error: {state.apiKeyError}</p>{/if}<pre>{state?.secretSetupCommand}</pre>{#if error}<p class="error-text">{error}</p>{/if}<div class="button-row"><button class="secondary" on:click={onRetry}>Retry key lookup</button></div></section></main>
{:else}
  <main class="setup-screen"><section class="setup-panel"><p class="eyebrow">Steam Achievement Tracker</p><h1>Connect your profile</h1><p class="muted">Paste a SteamID64, profile URL, or vanity name. Profile game details must be public.</p><div class="input-row"><input value={profileInput} placeholder="SteamID64, https://steamcommunity.com/id/name, or vanity" on:input={(e) => onProfileInput(e.currentTarget.value)} on:keydown={(e) => e.key === 'Enter' && onValidate()} /><button class="primary" on:click={onValidate} disabled={validating}>{validating ? 'Checking…' : 'Validate'}</button></div>{#if error}<p class="error-text">{error}</p>{/if}{#if preview}<section class="confirm-card">{#if preview.avatarUrl}<img src={preview.avatarUrl} alt="" />{:else}<div class="avatar-fallback">{(preview.displayName || preview.steamId64 || 'S').slice(0, 1).toUpperCase()}</div>{/if}<div class="confirm-copy"><p class="eyebrow">Profile found</p><h2>{preview.displayName}</h2><p class="muted">{preview.steamId64}</p></div><button class="primary confirm-action" on:click={onSave} disabled={saving}>{saving ? 'Saving…' : 'Save and sync'}</button></section>{/if}</section></main>
{/if}
