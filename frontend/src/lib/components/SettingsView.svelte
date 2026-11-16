<script>
  import {tick} from 'svelte'
  import ApiKeyForm from './ApiKeyForm.svelte'
  export let state
  export let clearingData = false
  export let savingSettings = false
  export let savingKey = false
  export let onClearData = () => {}
  export let onSetLibrarySettings = () => {}
  export let onSaveKey = () => {}

  let unvettedBox = null
  let playedFreeBox = null
  let showKeyForm = false
  let keySaved = false
  let replaceButton = null

  function toggleKeyForm() {
    showKeyForm = !showKeyForm
    if (showKeyForm) keySaved = false
  }

  // The form closes only on success; a failed save rejects through here and keeps it open with the error. On success
  // the status line confirms it and focus returns to the toggle that opened the form.
  async function replaceKey(key) {
    keySaved = false
    await onSaveKey(key)
    showKeyForm = false
    keySaved = true
    await tick()
    replaceButton?.focus()
  }

  $: includeUnvettedApps = state.settings?.includeUnvettedApps ?? true
  $: includePlayedFreeGames = state.settings?.includePlayedFreeGames ?? true

  // The switches are never `disabled` while saving: a disabled input dims, drops focus and flashes the focus ring.
  // Clicks (and Space) during a save are cancelled instead, so the stored value stays untouched.
  function blockWhileSaving(event) {
    if (savingSettings) event.preventDefault()
  }

  // The parent may reject a change (it keeps the old state), so after saving snap both boxes back to the stored values.
  async function changeLibrarySettings() {
    const nextUnvetted = unvettedBox.checked
    const nextPlayedFree = playedFreeBox.checked
    try {
      await onSetLibrarySettings(nextUnvetted, nextPlayedFree)
    } finally {
      await tick()
      if (unvettedBox) unvettedBox.checked = includeUnvettedApps
      if (playedFreeBox) playedFreeBox.checked = includePlayedFreeGames
    }
  }
</script>

<section class="settings-panel">
  <div class="section-head"><h2>Settings</h2></div>
  <dl><dt>Active profile</dt><dd>{state.profile?.displayName} ({state.profile?.steamId64})</dd><dt>Data directory</dt><dd>{state.dataDir}</dd><dt>Log file</dt><dd>{state.logFile}</dd></dl>
  <section class="api-key-section">
    <h3>Steam Web API key</h3>
    <p class="muted">Stored in Secret Service.</p>
    <div class="button-row"><button bind:this={replaceButton} class="secondary" disabled={savingKey} aria-expanded={showKeyForm} on:click={toggleKeyForm}>Replace key</button></div>
    {#if keySaved}<p class="muted" role="status">Key saved.</p>{/if}
    {#if showKeyForm}<ApiKeyForm saving={savingKey} autofocus onSave={replaceKey} />{/if}
  </section>
  <section class="library-sync" aria-labelledby="library-sync-heading">
    <h3 id="library-sync-heading">Library sync</h3>
    <div class="setting-row">
      <input id="include-unvetted-apps" type="checkbox" role="switch" bind:this={unvettedBox} checked={includeUnvettedApps} aria-busy={savingSettings} aria-describedby="include-unvetted-apps-hint" on:click={blockWhileSaving} on:change={changeLibrarySettings} />
      <div>
        <label for="include-unvetted-apps">Include unvetted apps</label>
        <p id="include-unvetted-apps-hint" class="muted">Steam hides new, not-yet-vetted apps by default.</p>
      </div>
    </div>
    <div class="setting-row">
      <input id="include-played-free-games" type="checkbox" role="switch" bind:this={playedFreeBox} checked={includePlayedFreeGames} aria-busy={savingSettings} aria-describedby="include-played-free-games-hint" on:click={blockWhileSaving} on:change={changeLibrarySettings} />
      <div>
        <label for="include-played-free-games">Include played free games</label>
        <p id="include-played-free-games-hint" class="muted">When off, only purchased games are tracked and the Purchased counter equals All games.</p>
      </div>
    </div>
    <p class="muted">Changes apply on next sync. Games no longer returned are marked as not owned.</p>
  </section>
  <section class="danger-zone"><div><h3>Clear active user data</h3><p class="muted">Deletes this active profile, tracked games, achievement data, sync history, manual flags, library sync settings, and cached game images. API key and logs stay. You can replace the key from Settings or the next setup screen.</p></div><button class="danger" on:click={onClearData} disabled={clearingData}>{clearingData ? 'Clearing…' : 'Clear data'}</button></section>
</section>
