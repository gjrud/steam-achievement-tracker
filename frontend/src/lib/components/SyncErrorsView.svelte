<script>
  import {formatDateTime} from '../format.js'
  import {gameTags} from '../tag.js'
  export let games = []
  export let viewSearch = ''
  export let tagLabel
  export let toneClass
  export let tagTone
  export let gameTitle
  export let gameWarning
  export let hasAchievements
  export let shouldShowSyncErrorMessage
  export let completionLabel
  export let pct
  export let gameActionLoading = false
  export let onOpenGameMenu
  export let onRunGameAction
</script>

<section class="section-block">
  <div class="section-head"><div><h2>Sync errors</h2><p class="section-copy">Games with current sync warnings. Previous data is kept when available.</p></div><span class="pill">{games.length} results</span></div>
  {#if games.length}
    <div class="sync-error-list">
      {#each games as game (game.appid)}
        <article class="sync-error-row" title={gameWarning(game) || gameTitle(game)} on:contextmenu|preventDefault={(event) => onOpenGameMenu(event, game)}>
          <div class="sync-error-main">
            <div class="sync-error-title"><span class="warning small" title={gameWarning(game)} aria-label={gameWarning(game)}>⚠</span><h3>{gameTitle(game)}</h3></div>
            <div class="sync-error-meta">{#each gameTags(game) as tag}<span class={`tag ${toneClass(tagTone(tag))}`}>{tagLabel(tag)}</span>{/each}{#if hasAchievements(game) || shouldShowSyncErrorMessage(game)}<span>{completionLabel(game)}</span>{/if}{#if hasAchievements(game)}<span>{pct(game.completionPercent)}</span>{/if}{#if game.lastSyncedAt}<span>Showing data from {formatDateTime(game.lastSyncedAt)}</span>{:else}<span>No previous sync data</span>{/if}{#if game.lastErrorAt}<span>Failed {formatDateTime(game.lastErrorAt)}</span>{/if}</div>
            {#if game.lastError}<p class="sync-error-detail">{game.lastError}</p>{/if}
          </div>
          <button class="secondary" on:click={() => onRunGameAction('disable', game)} disabled={gameActionLoading}>Disable</button>
        </article>
      {/each}
    </div>
  {:else}<p class="empty-state">{viewSearch ? 'No matching games.' : 'No sync errors.'}</p>{/if}
</section>
