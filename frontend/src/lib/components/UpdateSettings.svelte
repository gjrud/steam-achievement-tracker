<script>
  import {tick} from 'svelte'
  import {bannerVisible, checkLocked, settingsStatus, updateError} from '../update.js'
  // updates is the store from createUpdateStore (update.js). Without one, or when the updater is unavailable in this
  // build, the section is not rendered.
  let {updates = null} = $props()

  let checksBox = $state(null)

  let view = $derived(updates ? $updates : null)
  let show = $derived(Boolean(view?.loaded && !view.snapshot.unavailable))
  let snap = $derived(view?.snapshot)
  // Settings repeats the banner's text only while the banner is hidden; the sr-only line below is the one live region.
  let bannerShown = $derived(view ? bannerVisible(view) : false)
  let error = $derived(view && !bannerShown ? updateError(view) : '')
  let status = $derived(view ? settingsStatus(view) : '')
  let announced = $derived(bannerShown ? '' : status)
  // Check now also waits for a Check, an Install or a Restart call that is still in flight.
  let checkDisabled = $derived(!view || checkLocked(snap.phase) || Boolean(view.acting))
  let checksEnabled = $derived(snap?.checksEnabled ?? false)

  // A click during a save is refused, so the box keeps the pending value instead of flipping.
  function blockWhileSaving(event) {
    if (view?.savingChecks) event.preventDefault()
  }

  // Check now is aria-disabled, not disabled: a disabled button would lose focus on the click that starts a
  // check. So the click is guarded here instead.
  function runCheck() {
    if (!checkDisabled) updates.check()
  }

  // A rejected change leaves the old value: snap the box back to the stored one.
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
      <input id="update-checks-enabled" type="checkbox" role="switch" bind:this={checksBox} checked={checksEnabled} aria-busy={view.savingChecks} aria-describedby="update-checks-enabled-hint" onclick={blockWhileSaving} onchange={changeChecks} />
      <div>
        <label for="update-checks-enabled">Check for updates on startup</label>
        <p id="update-checks-enabled-hint" class="muted">Contacts GitHub shortly after the app starts. Nothing is downloaded until you choose Install.</p>
      </div>
    </div>
    {#if view.settingsError}<p class="error-text update-status" role="alert">{view.settingsError}</p>{/if}
    <div class="button-row update-check-row">
      <button class="secondary" aria-disabled={checkDisabled} onclick={runCheck}>Check now</button>
      <p class="update-status muted">{status}</p>
      <p class="sr-only" role="status">{announced}</p>
    </div>
    {#if error}<p class="error-text update-status" role="alert">{error}</p>{/if}
  </section>
{/if}
