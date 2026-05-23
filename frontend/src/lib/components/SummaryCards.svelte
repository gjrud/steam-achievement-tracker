<script>
  export let summary = {}
  export let syncIsRunning = false
  export let syncCardTone = ''
  export let latestSyncRun = null
  export let toneClass = (tone) => (tone ? `tone-${tone}` : '')
  export let syncTitle = (run, running) => ''
  export let syncDetails = (run, running) => ''
  export let integer = (value) => String(value)
</script>

<section class="summary-grid" aria-label="Summary">
  <article class="summary-card"><span>Owned games</span><strong>{integer(summary.ownedGamesCount)}</strong></article>
  <article class="summary-card"><span>Completed games</span><strong>{integer(summary.completedGamesCount)}</strong></article>
  <article class="summary-card"><span>Achievements</span><strong>{integer(summary.totalAchievementsUnlocked)}</strong></article>
  <article class={`summary-card wide ${toneClass(syncCardTone)}`} aria-live="polite">
    <span>{syncIsRunning ? 'Current sync' : 'Last sync'}</span>
    <strong>{syncTitle(latestSyncRun, syncIsRunning)}</strong>
    <small>{syncDetails(latestSyncRun, syncIsRunning)}</small>
  </article>
</section>
