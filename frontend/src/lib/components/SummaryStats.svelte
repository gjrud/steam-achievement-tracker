<script>
  export let summary = {}
  export let integer = (value) => String(value)
</script>

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
        <div class="stat"><dt>Completed games</dt><dd>{integer(summary.completedGamesCount)}</dd></div>
        <div class="stat"><dt>Completed sets</dt><dd>{integer(summary.completedSetsCount)}</dd></div>
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
