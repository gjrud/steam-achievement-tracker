<script>
  export let game
  export let onContextMenu = () => {}
  export let gameTags = (g) => g.tags || []
  export let tagLabel = (tag) => tag
  export let toneClass = (tone) => (tone ? `tone-${tone}` : '')
  export let tagTone = (tag) => ''
  export let gameTitle = (g) => g.name || 'Untitled game'
  export let gameWarning = () => ''
  export let hasAchievements = () => false
  export let shouldShowSyncErrorMessage = () => false
  export let cardProgress = () => 0
  export let completionLabel = () => ''
  export let formatPlaytime = (v) => v
  export let pct = (v) => v
  export let missingAvgUnlockLabel = () => ''
</script>

<article class="game-card" title={gameWarning(game) || gameTitle(game)} on:contextmenu|preventDefault={(event) => onContextMenu(event, game)}>
  <div class="cover">{#if game.coverUrl}<img src={game.coverUrl} alt="" loading="lazy" decoding="async" />{:else}<div class="cover-placeholder"><span>{gameTitle(game).slice(0, 1).toUpperCase()}</span><small>Missing cover</small></div>{/if}</div>
  <div class="game-body">
    <h3>{gameTitle(game)}</h3>
    <div class="tag-list">{#each gameTags(game) as tag}<span class={`tag ${toneClass(tagTone(tag))}`}>{tagLabel(tag)}</span>{/each}</div>
    {#if hasAchievements(game)}
      <div class="progress" aria-label={completionLabel(game)}><div class="bar" style={`width: ${cardProgress(game)}%`}></div><span>{pct(game.completionPercent)}</span></div>
      <div class="meta"><span>{completionLabel(game)}</span><span>{game.playtimeForever != null ? formatPlaytime(game.playtimeForever) : ''}</span></div>
      {#if missingAvgUnlockLabel(game)}<p class="missing">{missingAvgUnlockLabel(game)}</p>{/if}
    {:else if shouldShowSyncErrorMessage(game)}
      <p class="missing">Sync error</p>
    {:else}
      {#if Number(game.playtimeForever) > 0}<div class="meta playtime-only"><span>{formatPlaytime(game.playtimeForever)}</span></div>{/if}
    {/if}
  </div>
</article>
