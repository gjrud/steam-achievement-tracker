<script>
  import {tick} from 'svelte'
  import UpdateSettings from './UpdateSettings.svelte'
  // updates: the update store (lib/update.js). Optional; without it the Updates section is not shown.
  let {
    state,
    updates = null,
    clearingData = false,
    savingSettings = false,
    onClearData = () => {},
    onSetLibrarySettings = () => {}
  } = $props()

  let unvettedBox = null
  let playedFreeBox = null

  let includeUnvettedApps = $derived(state.settings?.includeUnvettedApps ?? true)
  let includePlayedFreeGames = $derived(state.settings?.includePlayedFreeGames ?? true)

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
  <dl>
    <dt>Active profile</dt><dd>{state.profile?.displayName} ({state.profile?.steamId64})</dd>
    <dt>Data directory</dt><dd>{state.dataDir}</dd>
    <dt>Cache directory</dt><dd>{state.cacheDir}</dd>
    <dt>Log directory</dt><dd>{state.logDir}</dd>
    <dt>Log file</dt><dd>{state.logFile}</dd>
  </dl>
  <section class="library-sync" aria-labelledby="library-sync-heading">
    <h3 id="library-sync-heading">Library sync</h3>
    <div class="setting-row">
      <input id="include-unvetted-apps" type="checkbox" role="switch" bind:this={unvettedBox} checked={includeUnvettedApps} aria-busy={savingSettings} aria-describedby="include-unvetted-apps-hint" onclick={blockWhileSaving} onchange={changeLibrarySettings} />
      <div>
        <label for="include-unvetted-apps">Include unvetted apps</label>
        <p id="include-unvetted-apps-hint" class="muted">Steam hides new, not-yet-vetted apps by default.</p>
      </div>
    </div>
    <div class="setting-row">
      <input id="include-played-free-games" type="checkbox" role="switch" bind:this={playedFreeBox} checked={includePlayedFreeGames} aria-busy={savingSettings} aria-describedby="include-played-free-games-hint" onclick={blockWhileSaving} onchange={changeLibrarySettings} />
      <div>
        <label for="include-played-free-games">Include played free games</label>
        <p id="include-played-free-games-hint" class="muted">When off, only purchased games are tracked and the Purchased counter equals All games.</p>
      </div>
    </div>
    <p class="muted">Changes apply on next sync. Games no longer returned are marked as not owned.</p>
  </section>
  <UpdateSettings {updates} />
  <section class="danger-zone"><div><h3>Remove active profile</h3><p class="muted">Remove the active profile. You choose whether to also delete its data and the API key.</p></div><button class="danger" onclick={onClearData} disabled={clearingData}>{clearingData ? 'Removing…' : 'Remove profile'}</button></section>
</section>
