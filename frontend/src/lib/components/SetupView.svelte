<script>
  import ApiKeyForm from './ApiKeyForm.svelte'
  let {
    mode = 'profile',
    // Prop `state` is renamed locally: a binding named `state` would shadow the `$state` rune.
    state: appState,
    error = '',
    profileInput = '',
    preview = null,
    validating = false,
    saving = false,
    savingKey = false,
    onRetry = () => {},
    onValidate = () => {},
    onSave = () => {},
    onSaveKey = () => {},
    onProfileInput = () => {}
  } = $props()

  let showKeyForm = $state(false)

  // A failed save keeps the form open; on success it closes.
  async function saveKey(key) {
    await onSaveKey(key)
    showKeyForm = false
  }
</script>

{#if mode === 'startup-error'}
  <main class="setup-screen setup-error"><section class="setup-panel"><p class="eyebrow">Steam Achievement Tracker</p><h1>Startup failed</h1><p class="muted">{appState.initError}</p>{#if appState.logFile}<p class="muted">Check {appState.logFile}.</p>{:else if appState.dataDir}<p class="muted">App folder: {appState.dataDir}</p>{/if}{#if error}<p class="error-text">{error}</p>{/if}</section></main>
{:else if mode === 'missing-key'}
  <main class="setup-screen"><section class="setup-panel"><p class="eyebrow">Steam Achievement Tracker</p><h1>Steam Web API key missing</h1>{#if appState?.apiKeyError}<p class="error-text">{appState.apiKeyError}</p>{/if}<p class="muted">Paste your Steam Web API key. It is stored only in your system keyring (Secret Service on Linux, Credential Manager on Windows).</p><div class="api-key-setup"><ApiKeyForm saving={savingKey} autofocus onSave={onSaveKey} /></div>{#if error}<p class="error-text">{error}</p>{/if}<div class="button-row"><button class="secondary" onclick={onRetry} disabled={savingKey}>Retry key lookup</button></div></section></main>
{:else}
  <main class="setup-screen"><section class="setup-panel"><p class="eyebrow">Steam Achievement Tracker</p><h1>Connect your profile</h1><p class="muted">Paste the SteamID64, profile URL, or vanity name of the account that owns the API key. Any privacy setting works.</p><div class="input-row"><input value={profileInput} placeholder="SteamID64, https://steamcommunity.com/id/name, or vanity" oninput={(e) => onProfileInput(e.currentTarget.value)} onkeydown={(e) => e.key === 'Enter' && onValidate()} /><button class="primary" onclick={onValidate} disabled={validating || savingKey}>{validating ? 'Checking…' : 'Validate'}</button></div>{#if error}<p class="error-text">{error}</p>{/if}{#if preview}<section class="confirm-card">{#if preview.avatarUrl}<img src={preview.avatarUrl} alt="" />{:else}<div class="avatar-fallback">{(preview.displayName || preview.steamId64 || 'S').slice(0, 1).toUpperCase()}</div>{/if}<div class="confirm-copy"><p class="eyebrow">Profile found</p><h2>{preview.displayName}</h2><p class="muted">{preview.steamId64}</p></div><button class="primary confirm-action" onclick={onSave} disabled={saving || savingKey}>{saving ? 'Saving…' : 'Save and sync'}</button></section>{/if}<div class="api-key-setup"><div class="button-row"><button class="secondary" disabled={savingKey} aria-expanded={showKeyForm} onclick={() => (showKeyForm = !showKeyForm)}>Replace key</button></div>{#if showKeyForm}<ApiKeyForm saving={savingKey} disabled={validating || saving} autofocus showLabel onSave={saveKey} />{/if}</div></section></main>
{/if}
