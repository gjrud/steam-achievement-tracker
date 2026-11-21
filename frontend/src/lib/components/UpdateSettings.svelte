<script>
  import {tick} from 'svelte'
  import {bannerVisible, checkLocked, settingsStatus, updateError} from '../update.js'
  // updates is the store from createUpdateStore (update.js). Without one, or when the updater is unavailable in this
  // build, the section is not rendered.
  export let updates = null

  let checksBox = null

  $: view = updates ? $updates : null
  $: show = Boolean(view?.loaded && !view.snapshot.unavailable)
  $: snap = view?.snapshot
  // The banner shows the same error and phase text while it is on screen, so Settings repeats them only when the banner
  // is hidden. The visible status line is not live; the announcer below is the one live region, and it stays silent
  // while the banner announces.
  $: bannerShown = view ? bannerVisible(view) : false
  $: error = view && !bannerShown ? updateError(view) : ''
  $: status = view ? settingsStatus(view) : ''
  $: announced = bannerShown ? '' : status
  // Check now also waits for a Check, an Install or a Restart call that is still in flight.
  $: checkDisabled = !view || checkLocked(snap.phase) || Boolean(view.acting)
  $: checksEnabled = snap?.checksEnabled ?? false

  // A click during a save is refused, so the box keeps the pending value instead of flipping.
  function blockWhileSaving(event) {
    if (view?.savingChecks) event.preventDefault()
  }

  // Check now is aria-disabled, not disabled: a disabled button would drop keyboard focus on the click that starts a
  // check. So the click is guarded here instead.
  function runCheck() {
    if (!checkDisabled) updates.check()
  }

  // Like the library switches: the box shows the stored value, so after the call it snaps back to what the snapshot says
  // (a rejected change leaves the old value).
  async function changeChecks() {
    try {
      await updates.setChecksEnabled(checksBox.checked)
    } finally {
      await tick()
      if (checksBox) checksBox.checked = checksEnabled
    }
  }
</script>

{#if show}
  <section class="library-sync update-settings" aria-labelledby="update-settings-heading">
    <h3 id="update-settings-heading">Updates</h3>
    <p class="update-current">Current version: {snap.currentVersion || 'unknown'}</p>
    <div class="setting-row">
      <input id="update-checks-enabled" type="checkbox" role="switch" bind:this={checksBox} checked={checksEnabled} aria-busy={view.savingChecks} aria-describedby="update-checks-enabled-hint" on:click={blockWhileSaving} on:change={changeChecks} />
      <div>
        <label for="update-checks-enabled">Check for updates on startup</label>
        <p id="update-checks-enabled-hint" class="muted">Contacts GitHub shortly after the app starts. Nothing is downloaded until you choose Install.</p>
      </div>
    </div>
    {#if view.settingsError}<p class="error-text update-status" role="alert">{view.settingsError}</p>{/if}
    <div class="button-row update-check-row">
      <button class="secondary" aria-disabled={checkDisabled} on:click={runCheck}>Check now</button>
      <p class="update-status muted">{status}</p>
      <p class="sr-only" role="status">{announced}</p>
    </div>
    {#if error}<p class="error-text update-status" role="alert">{error}</p>{/if}
  </section>
{/if}
