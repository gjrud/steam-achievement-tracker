<script>
  import {onMount} from 'svelte'
  import {syncBand} from '../sync.js'
  export let state
  export let latestSyncRun = null
  export let syncIsRunning = false
  export let onSyncNow = () => {}

  // now is refreshed every 30 seconds so relative times ("2 hours ago") stay current.
  let now = Date.now()
  onMount(() => {
    const clock = setInterval(() => {
      now = Date.now()
    }, 30000)
    return () => clearInterval(clock)
  })

  $: band = syncBand(latestSyncRun, syncIsRunning, now)
</script>

<div class={`profile-card tone-${band.tone}`}>
  <div class="profile-avatar">{#if state.profile?.avatarUrl}<img src={state.profile.avatarUrl} alt="" />{:else}{(state.profile?.displayName || 'S').slice(0, 1).toUpperCase()}{/if}</div>
  <p class="profile-name">{state.profile?.displayName}</p>
  <button class="primary" on:click={onSyncNow} disabled={state.syncInProgress}>{state.syncInProgress ? 'Syncing…' : 'Sync now'}</button>
  <div class="sync-band">
    <p class="sync-headline">
      {#if band.icon === 'check'}
        <svg class="ico" data-icon="check" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M3.5 8.5l3 3 6-7" /></svg>
      {:else if band.icon === 'spin'}
        <svg class="ico spin" data-icon="spin" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" aria-hidden="true"><path d="M8 2a6 6 0 1 0 6 6" /></svg>
      {:else if band.icon === 'clock'}
        <svg class="ico" data-icon="clock" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" aria-hidden="true"><circle cx="8" cy="8" r="6.2" /><path d="M8 4.6V8l2.3 1.5" /></svg>
      {:else if band.icon === 'warn'}
        <svg class="ico" data-icon="warn" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M8 2.2l6.2 11H1.8z" /><path d="M8 6.6v3.2M8 11.6v.1" /></svg>
      {:else}
        <svg class="ico" data-icon="stop" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" aria-hidden="true"><circle cx="8" cy="8" r="6.2" /><path d="M3.7 12.3l8.6-8.6" /></svg>
      {/if}
      <span>{band.headline}</span>
    </p>
    {#if band.detail || band.time}
      <p class="sync-meta">{band.detail}{#if band.detail && band.time}{' · '}{/if}{#if band.time}<time datetime={band.time.iso} title={band.time.title}>{band.time.text}</time>{/if}</p>
    {/if}
    <p class="sr-only" role="status">{band.announce}</p>
  </div>
</div>
