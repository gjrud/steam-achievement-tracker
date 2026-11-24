<script>
  import {tagDescription} from '../tag.js'
  import {cssUrl} from '../game.js'

  // onContextMenu(event, game): event is the right click (clientX/clientY).
  let {
    game,
    onContextMenu = () => {},
    gameTags = (g) => g.tags || [],
    tagLabel = (tag) => tag,
    toneClass = (tone) => (tone ? `tone-${tone}` : ''),
    tagTone = (tag) => '',
    gameTitle = (g) => g.name || 'Untitled game',
    gameWarning = () => '',
    hasAchievements = () => false,
    shouldShowSyncErrorMessage = () => false,
    cardProgress = () => 0,
    completionLabel = () => '',
    formatPlaytime = (v) => v,
    pct = (v) => v,
    missingAvgUnlockLabel = () => ''
  } = $props()

  let landscape = $derived(Boolean(game.coverUrl) && (game.coverKind === 'header' || game.coverKind === 'main'))
  let coverStyle = $derived(game.coverUrl ? `--cover-url: ${cssUrl(game.coverUrl)}` : undefined)
</script>

<article
  class="game-card"
  title={gameWarning(game) || gameTitle(game)}
  oncontextmenu={(event) => {
    event.preventDefault()
    onContextMenu(event, game)
  }}
>
  <div
    class="cover"
    class:cover-landscape={landscape}
    style={coverStyle}
  >{#if game.coverUrl}<img src={game.coverUrl} alt="" loading="lazy" decoding="async" />{:else}<div class="cover-placeholder"><span>{gameTitle(game).slice(0, 1).toUpperCase()}</span><small>Missing cover</small></div>{/if}</div>
  <div class="game-body">
    <h3>{gameTitle(game)}</h3>
    <div class="tag-list">{#each gameTags(game) as tag}<span class={`tag ${toneClass(tagTone(tag))}`} title={tagDescription(tag) || undefined}>{tagLabel(tag)}</span>{/each}</div>
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
