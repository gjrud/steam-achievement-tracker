<script>
  import {Browser} from '@wailsio/runtime'
  import {bannerVisible, downloadFraction, formatBytes, readyLine, retryKind, updateError} from '../update.js'
  // updates is the store from createUpdateStore (update.js). Without one the banner renders nothing.
  let {updates = null} = $props()

  let linkError = $state('')

  let view = $derived(updates ? $updates : null)
  let show = $derived(view ? bannerVisible(view) : false)
  let snap = $derived(view?.snapshot)
  let phase = $derived(snap?.phase)
  let latest = $derived(snap?.latest)
  let version = $derived(latest?.version || '')
  let error = $derived(view ? updateError(view) : '')
  let acting = $derived(view?.acting || '')
  let canceling = $derived(Boolean(view?.canceling))
  let retry = $derived(view ? retryKind(view) : 'check')
  let fraction = $derived(phase === 'downloading' ? downloadFraction(snap.progress) : null)
  let percent = $derived(fraction === null ? 0 : Math.round(fraction * 100))
  // The error phase and a refusal note keep the banner's colour; ready is the good-news colour.
  let tone = $derived(phase === 'error' || (error && phase !== 'ready') ? 'is-error' : phase === 'ready' ? 'is-ready' : '')
  let meter = $derived(phase === 'downloading' || phase === 'verifying' || phase === 'installing' || phase === 'restarting')
  let determinate = $derived(phase === 'downloading' && fraction !== null)
  let releaseLabel = $derived(phase === 'error' || snap?.notifyOnly ? 'Release page' : 'Release notes')
  // The release link: an available release always; an error only when a retry installs that release or the build is
  // notify-only. A failed check does not point at a release it did not find.
  let releaseLink = $derived(Boolean(latest) && (phase === 'available' || (phase === 'error' && (retry === 'install' || Boolean(snap?.notifyOnly)))))

  let title = $derived(titleFor(phase, version, retry))
  let detail = $derived(detailFor(phase, snap))
  let progressText = $derived(phase === 'downloading' ? downloadText(snap.progress, fraction, percent) : '')
  // Why a notify-only build offers no Install; announced with the title.
  let notifyNote = $derived(phase === 'available' && snap?.notifyOnly ? detail : '')
  // The one live announcement. Progress is left out, so a screen reader never hears each download tick.
  let announcement = $derived(show ? [title, notifyNote, error, view?.notice, linkError].filter(Boolean).map(sentence).join(' ') : '')

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

<div class="update-host">
  {#if show}
    <section class={`update-banner ${tone}`} aria-label="Application update">
      <div class="update-copy">
        <p class="update-title">{title}</p>
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
          <a class="update-link" href={latest.releaseURL} rel="noopener noreferrer" onclick={(event) => {
              event.preventDefault()
              openRelease(latest.releaseURL)
            }}>{releaseLabel}</a>
        {/if}
        {#if phase === 'available'}
          {#if !snap.notifyOnly}
            <button class="primary" disabled={Boolean(acting)} onclick={() => updates.install(version)}>Install</button>
          {/if}
          <button class="secondary" onclick={() => updates.later()}>Later</button>
        {:else if phase === 'downloading'}
          <button class="secondary" disabled={canceling} onclick={() => updates.cancel()}>{canceling ? 'Canceling…' : 'Cancel'}</button>
        {:else if phase === 'ready'}
          <button class="primary" disabled={Boolean(acting)} aria-describedby="update-detail" onclick={() => updates.restart()}>Restart now</button>
        {:else if phase === 'error'}
          <button class="primary" disabled={Boolean(acting)} onclick={() => (retry === 'install' ? updates.install(version) : updates.check())}>Try again</button>
        {/if}
      </div>
    </section>
  {/if}
  <p class="sr-only" role="status">{announcement}</p>
</div>
