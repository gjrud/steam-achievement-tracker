<script>
  import {syncDetails, syncTitle} from '../sync.js'
  export let state
  export let latestSyncRun = null
  export let syncIsRunning = false
  export let onSyncNow = () => {}

  $: syncTone = syncIsRunning
    ? 'run'
    : !latestSyncRun
      ? 'none'
      : latestSyncRun.status === 'partial' || latestSyncRun.status === 'failed'
        ? 'warn'
        : 'ok'
</script>

<div class="profile-card">
  <div class="profile-avatar">{#if state.profile?.avatarUrl}<img src={state.profile.avatarUrl} alt="" />{:else}{(state.profile?.displayName || 'S').slice(0, 1).toUpperCase()}{/if}</div>
  <p class="profile-name">{state.profile?.displayName}</p>
  <button class="primary" on:click={onSyncNow} disabled={state.syncInProgress}>{state.syncInProgress ? 'Syncing…' : 'Sync now'}</button>
  <div class="profile-sync" aria-live="polite">
    <span class={`sync-dot ${syncTone}`} aria-hidden="true"></span>
    <p class="sync-line"><span class="sr-only">{syncIsRunning ? 'Current sync' : 'Last sync'}</span>{' '}{syncTitle(latestSyncRun, syncIsRunning)}</p>
    <small class="sync-detail">{syncDetails(latestSyncRun, syncIsRunning)}</small>
  </div>
</div>
