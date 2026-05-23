<script>
  export let games = []
  export let viewSearch = ''
  export let gameTitle
  export let gameWarning
  export let hasAchievements
  export let completionLabel
  export let pct
  export let formatPlaytime
  export let onRunGameAction
  export let showDisabledReasonLabel
</script>

<section class="section-block">
  <div class="section-head"><div><h2>Disabled games</h2><p class="section-copy">Hidden from suggestions, completed games, warnings, and summary totals.</p></div><span class="pill">{games.length} results</span></div>
  {#if games.length}
    <div class="disabled-list">
      {#each games as game (game.appid)}
        <article class="disabled-row" title={gameWarning(game) || gameTitle(game)}>
          <div class="disabled-main"><h3>{gameTitle(game)}</h3><div class="disabled-meta"><span class="badge tone-neutral">{showDisabledReasonLabel()}</span><span>{completionLabel(game)}</span>{#if hasAchievements(game)}<span>{pct(game.completionPercent)}</span>{/if}{#if game.playtimeForever != null}<span>{formatPlaytime(game.playtimeForever)}</span>{/if}{#if game.syncWarning}<span class="warning small" title={gameWarning(game)} aria-label={gameWarning(game)}>⚠</span>{/if}</div></div>
          <button class="secondary" on:click={() => onRunGameAction('enable', game)}>Re-enable</button>
        </article>
      {/each}
    </div>
  {:else}<p class="empty-state">{viewSearch ? 'No matching games.' : 'No disabled games.'}</p>{/if}
</section>
