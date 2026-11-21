<script>
  import {tick} from 'svelte'
  import {Browser} from '@wailsio/runtime'
  import {bannerVisible, downloadFraction, formatBytes, readyLine, retryKind, updateError} from '../update.js'
  // updates is the store from createUpdateStore (update.js). Without one the banner renders nothing.
  export let updates = null

  let linkError = ''
  // host is the focus target when the banner's own controls go away. It stays mounted while the banner is hidden.
  let host = null
  let titleEl = null
  let focusPending = false

  $: view = updates ? $updates : null
  $: show = view ? bannerVisible(view) : false
  $: snap = view?.snapshot
  $: phase = snap?.phase
  $: latest = snap?.latest
  $: version = latest?.version || ''
  $: error = view ? updateError(view) : ''
  $: acting = view?.acting || ''
  $: canceling = Boolean(view?.canceling)
  $: retry = view ? retryKind(view) : 'check'
  $: fraction = phase === 'downloading' ? downloadFraction(snap.progress) : null
  $: percent = fraction === null ? 0 : Math.round(fraction * 100)
  // The error phase and a refusal note keep the banner's colour; ready is the good-news colour.
  $: tone = phase === 'error' || (error && phase !== 'ready') ? 'is-error' : phase === 'ready' ? 'is-ready' : ''
  $: meter = phase === 'downloading' || phase === 'verifying' || phase === 'installing' || phase === 'restarting'
  $: determinate = phase === 'downloading' && fraction !== null
  $: releaseLabel = phase === 'error' || snap?.notifyOnly ? 'Release page' : 'Release notes'
  // The release link: an available release always; an error only when a retry installs that release or the build is
  // notify-only. A failed check does not point at a release it did not find.
  $: releaseLink = Boolean(latest) && (phase === 'available' || (phase === 'error' && (retry === 'install' || Boolean(snap?.notifyOnly))))

  $: title = titleFor(phase, version, retry)
  $: detail = detailFor(phase, snap)
  $: progressText = phase === 'downloading' ? downloadText(snap.progress, fraction, percent) : ''
  // notifyNote is the reason a notify-only build offers no Install. It is the message of that phase, so it is announced
  // with the title.
  $: notifyNote = phase === 'available' && snap?.notifyOnly ? detail : ''
  // The one live announcement. Its text covers the title, the notify reason, the error, the note and the link error; the
  // progress line is left out, and the title does not change during a download, so progress never reaches a screen reader.
  $: announcement = show ? [title, notifyNote, error, view?.notice, linkError].filter(Boolean).map(sentence).join(' ') : ''
  // A click disables or removes the button it came from, and that drops keyboard focus to the body. Each change that
  // follows a click (the call starting, the download replacing the button, Later hiding the banner) settles focus.
  $: settleFocus(phase, acting, canceling, show)
  $: leaveBanner(show)

  function titleFor(p, v, kind) {
    switch (p) {
      case 'checking':
        return 'Checking for updates…'
      case 'available':
        return `Version ${v} is available`
      case 'downloading':
        return `Downloading version ${v}`
      case 'verifying':
        return 'Verifying download…'
      case 'installing':
        return 'Installing update…'
      case 'ready':
        return readyLine(v)
      case 'restarting':
        return 'Restarting…'
      case 'error':
        return kind === 'install' ? `Could not update to version ${v}` : 'Could not check for updates'
      default:
        return ''
    }
  }

  function detailFor(p, s) {
    if (!s) return ''
    if (p === 'available') {
      if (s.notifyOnly) return s.notifyReason
      const size = s.latest?.size > 0 ? ` Download size ${formatBytes(s.latest.size)}.` : ''
      return `You have version ${s.currentVersion}.${size}`
    }
    if (p === 'ready') return 'Restarting closes the app and cancels a running sync. The update then applies.'
    if (p === 'restarting') return 'This can take up to 30 seconds.'
    return ''
  }

  function downloadText(progress, f, pct) {
    if (f === null) return progress.written > 0 ? `${formatBytes(progress.written)} downloaded` : 'Preparing download…'
    return `${formatBytes(progress.written)} of ${formatBytes(progress.total)} (${pct}%)`
  }

  // sentence ends a line with a full stop unless it already ends in punctuation, so the joined announcement reads cleanly.
  function sentence(text) {
    return /[.!?…]$/.test(text) ? text : `${text}.`
  }

  // act marks that a click started the change, so focus is settled once the change shows.
  function act(run) {
    focusPending = true
    return run()
  }

  // leaveBanner runs as the banner goes. Its DOM is removed right after, so a focused control or title inside it would
  // drop focus to the body; focus moves to the host first.
  function leaveBanner(visible) {
    if (visible || !host) return
    const active = document.activeElement
    if (active && active !== host && host.contains(active)) host.focus()
  }

  // settleFocus runs after a change that followed a click. Once the DOM has caught up, focus that is still on an enabled
  // control in the banner stays put; otherwise it moves to the title, or to the host when the banner is gone.
  async function settleFocus() {
    if (!focusPending) return
    await tick()
    if (!host) return
    const active = document.activeElement
    if (active && active !== document.body && host.contains(active) && !active.disabled) {
      focusPending = false
      return
    }
    focusPending = false
    if (show && titleEl) titleEl.focus()
    else host.focus()
  }

  // A plain <a href> would navigate the webview itself, so clicks go to the system browser (as in ApiKeyForm); the real
  // href stays for accessibility and copy/paste. A failed open is shown inline.
  async function openRelease(url) {
    linkError = ''
    try {
      await Browser.OpenURL(url)
    } catch {
      linkError = `Could not open the browser. Visit ${url} to read it.`
    }
  }
</script>

<div class="update-host" bind:this={host} tabindex="-1">
  {#if show}
    <section class={`update-banner ${tone}`} aria-label="Application update">
      <div class="update-copy">
        <p class="update-title" tabindex="-1" bind:this={titleEl}>{title}</p>
        {#if detail}<p id="update-detail" class="update-detail">{detail}</p>{/if}
        {#if phase === 'downloading'}
          <p class="update-progress-text" aria-live="off">{progressText}</p>
        {/if}
        {#if meter}
          <div
            class="update-meter"
            class:indeterminate={!determinate}
            role="progressbar"
            aria-label={phase === 'downloading' ? 'Download progress' : 'Update progress'}
            aria-valuemin={determinate ? 0 : undefined}
            aria-valuemax={determinate ? 100 : undefined}
            aria-valuenow={determinate ? percent : undefined}
            aria-valuetext={phase === 'downloading' ? progressText : title}
          ><span class="update-meter-fill" style={determinate ? `width: ${percent}%` : undefined}></span></div>
        {/if}
        {#if error}<p class="update-error-text error-text">{error}</p>{/if}
        {#if view?.notice}<p class="update-detail">{view.notice}</p>{/if}
        {#if linkError}<p class="update-error-text error-text">{linkError}</p>{/if}
      </div>
      <div class="update-actions">
        {#if releaseLink}
          <a class="update-link" href={latest.releaseURL} rel="noopener noreferrer" on:click|preventDefault={() => openRelease(latest.releaseURL)}>{releaseLabel}</a>
        {/if}
        {#if phase === 'available'}
          {#if !snap.notifyOnly}
            <button class="primary" disabled={Boolean(acting)} on:click={() => act(() => updates.install(version))}>Install</button>
          {/if}
          <button class="secondary" on:click={() => act(() => updates.later())}>Later</button>
        {:else if phase === 'downloading'}
          <button class="secondary" disabled={canceling} on:click={() => act(() => updates.cancel())}>{canceling ? 'Canceling…' : 'Cancel'}</button>
        {:else if phase === 'ready'}
          <button class="primary" disabled={Boolean(acting)} aria-describedby="update-detail" on:click={() => act(() => updates.restart())}>Restart now</button>
        {:else if phase === 'error'}
          <button class="primary" disabled={Boolean(acting)} on:click={() => act(() => (retry === 'install' ? updates.install(version) : updates.check()))}>Try again</button>
        {/if}
      </div>
    </section>
  {/if}
  <p class="sr-only" role="status">{announcement}</p>
</div>
