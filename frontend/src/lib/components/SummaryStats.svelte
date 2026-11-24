<script>
  let {summary = {}, integer = (value) => String(value)} = $props()

  // A counter shows "vetted (total)" only when at least one counted entry is unvetted (vetted < total).
  // A missing vetted field means every entry is vetted, so the plain total shows.
  function hasUnvetted(total, vetted) {
    return typeof total === 'number' && typeof vetted === 'number' && vetted < total
  }

  function vettedLabel(total, vetted) {
    return hasUnvetted(total, vetted) ? `${integer(vetted)} vetted of ${integer(total)} total` : undefined
  }

  // The hover hint (title) spells out the vetted count; the screen-reader text comes from the sr-only span in the counter.
  let completedGamesLabel = $derived(vettedLabel(summary.completedGamesCount, summary.completedGamesVettedCount))
  let completedSetsLabel = $derived(vettedLabel(summary.completedSetsCount, summary.completedSetsVettedCount))
</script>

{#snippet counter(total, vetted)}
  {#if hasUnvetted(total, vetted)}
    <span class="vetted">{integer(vetted)}</span><span class="sr-only">{' vetted of '}</span><span class="vetted-total">({integer(total)})</span>
  {:else}
    {integer(total)}
  {/if}
{/snippet}

<section class="summary-stats" aria-label="Summary">
  <div class="stat-groups">
    <div class="stat-group">
      <span class="stat-group-label">Library</span>
      <dl class="stat-list">
        <div class="stat"><dt>All games</dt><dd>{integer(summary.ownedGamesCount)}</dd></div>
        <div class="stat"><dt>Purchased</dt><dd>{integer(summary.purchasedGamesCount)}</dd></div>
      </dl>
    </div>
    <div class="stat-group">
      <span class="stat-group-label">Completed</span>
      <dl class="stat-list">
        <div class="stat"><dt>Completed games</dt><dd title={completedGamesLabel}>{@render counter(summary.completedGamesCount, summary.completedGamesVettedCount)}</dd></div>
        <div class="stat"><dt>Completed sets</dt><dd title={completedSetsLabel}>{@render counter(summary.completedSetsCount, summary.completedSetsVettedCount)}</dd></div>
      </dl>
    </div>
    <div class="stat-group">
      <span class="stat-group-label">Unlocked</span>
      <dl class="stat-list">
        <div class="stat"><dt>Achievements</dt><dd>{integer(summary.totalAchievementsUnlocked)}</dd></div>
      </dl>
    </div>
  </div>
</section>
