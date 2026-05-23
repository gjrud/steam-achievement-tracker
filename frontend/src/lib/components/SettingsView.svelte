<script>
  export let state
  export let clearingData = false
  export let onClearData = () => {}
</script>

<section class="settings-panel">
  <div class="section-head"><h2>Settings</h2></div>
  <dl><dt>Active profile</dt><dd>{state.profile?.displayName} ({state.profile?.steamId64})</dd><dt>Data directory</dt><dd>{state.dataDir}</dd><dt>Log file</dt><dd>{state.logFile}</dd><dt>Secret Service key</dt><dd>{state.apiKeyPresent ? 'Present' : 'Missing'}</dd></dl>
  <section class="danger-zone"><div><h3>Clear active user data</h3><p class="muted">Deletes this active profile, tracked games, achievement data, sync history, manual flags, and cached game images. API key and logs stay.</p></div><button class="danger" on:click={onClearData} disabled={clearingData}>{clearingData ? 'Clearing…' : 'Clear data'}</button></section>
</section>
