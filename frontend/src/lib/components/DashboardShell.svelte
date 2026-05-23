<script>
  import SummaryCards from './SummaryCards.svelte'
  import GameCard from './GameCard.svelte'
  import SyncErrorsView from './SyncErrorsView.svelte'
  import DisabledView from './DisabledView.svelte'
  import SettingsView from './SettingsView.svelte'
  import {gameTags} from '../tag.js'
  export let state
  export let error = ''
  export let summary = {}
  export let latestSyncRun = null
  export let syncIsRunning = false
  export let syncCardTone = ''
  export let activeView = 'suggestions'
  export let viewSearch = ''
  export let activeTagFilter = ''
  export let viewSearchInput = null
  export let suggestions = []
  export let suggestionTagCounts = {}
  export let visibleSuggestionTagOptions = []
  export let filteredSuggestions = []
  export let filteredCompleted = []
  export let filteredWarnings = []
  export let filteredDisabled = []
  export let suggestionsFiltered = false
  export let gameActionLoading = false
  export let clearingData = false
  export let onSyncNow = () => {}
  export let onActiveViewChange = () => {}
  export let onViewSearch = () => {}
  export let onActiveTagFilter = () => {}
  export let onOpenGameMenu = () => {}
  export let onRunGameAction = () => {}
  export let onClearData = () => {}
  export let gameTitle
  export let gameWarning
  export let formatPlaytime
  export let toneClass
  export let tagTone
  export let tagLabel
  export let integer
  export let pct
  export let cardProgress
  export let completionLabel
  export let missingAvgUnlockLabel
  export let hasAchievements
  export let shouldShowSyncErrorMessage
  export let showDisabledReasonLabel
  export let syncTitle
  export let syncDetails

</script>

<main class="app-shell">
  <header class="topbar"><div class="title-block"><p class="eyebrow large">Steam Achievement Tracker</p><h1 class="sr-only">Dashboard</h1><p class="title-subtitle">Spot new achievements without the hunt.</p></div><div class="profile-block"><div class="profile-identity">{#if state.profile?.avatarUrl}<img src={state.profile.avatarUrl} alt="" />{:else}<div class="avatar-fallback small">{(state.profile?.displayName || 'S').slice(0, 1).toUpperCase()}</div>{/if}<div class="profile-copy"><p class="profile-name">{state.profile?.displayName}</p></div></div><button class="primary" on:click={onSyncNow} disabled={state.syncInProgress}>{state.syncInProgress ? 'Syncing…' : 'Sync now'}</button></div></header>
  {#if error}<div class="banner error-banner" aria-live="polite">{error}</div>{/if}
  <SummaryCards {summary} {syncIsRunning} {syncCardTone} {latestSyncRun} {toneClass} {syncTitle} {syncDetails} {integer} />
  <nav class="tabs" aria-label="Dashboard views"><button class:active={activeView === 'suggestions'} on:click={() => onActiveViewChange('suggestions')}>Not completed</button><button class:active={activeView === 'completed'} on:click={() => onActiveViewChange('completed')}>Completed</button><button class:active={activeView === 'warnings'} on:click={() => onActiveViewChange('warnings')}>Sync errors</button><button class:active={activeView === 'disabled'} on:click={() => onActiveViewChange('disabled')}>Disabled</button><button class:active={activeView === 'settings'} on:click={() => onActiveViewChange('settings')}>Settings</button></nav>
  {#if activeView !== 'settings'}
    <section class="view-toolbar" aria-label="Search current view"><input bind:this={viewSearchInput} class="search-input" type="search" placeholder="Search games in this view" bind:value={viewSearch} on:input={(e) => onViewSearch(e.currentTarget.value)} />{#if activeView === 'suggestions' && visibleSuggestionTagOptions.length}<div class="tag-filter-panel" aria-label="Filter not completed games by tag"><div class="tag-filter-heading"><span>Filter by tag</span>{#if activeTagFilter}<button class="link-button" on:click={() => onActiveTagFilter('')}>Clear tag filter</button>{/if}</div><div class="tag-filter-list"><button class="tag tag-filter" class:active={!activeTagFilter} on:click={() => onActiveTagFilter('')}>All <span>{integer(suggestions.length)}</span></button>{#each visibleSuggestionTagOptions as tag}<button class={`tag tag-filter ${toneClass(tagTone(tag))}`} class:active={activeTagFilter === tag} on:click={() => onActiveTagFilter(tag)}>{tagLabel(tag)} <span>{integer(suggestionTagCounts[tag])}</span></button>{/each}</div></div>{/if}</section>
  {/if}
  {#if activeView === 'suggestions'}
    {#if filteredSuggestions.length}<div class="game-grid incomplete-grid">{#each filteredSuggestions as game (game.appid)}<GameCard {game} {gameTags} {tagLabel} {toneClass} {tagTone} {gameTitle} {gameWarning} {hasAchievements} {shouldShowSyncErrorMessage} {cardProgress} {completionLabel} {formatPlaytime} {pct} {missingAvgUnlockLabel} onContextMenu={onOpenGameMenu} />{/each}</div>{:else}<p class="empty-state">{suggestionsFiltered ? 'No games match the current filters.' : 'No incomplete games found yet.'}</p>{/if}
  {:else if activeView === 'completed'}
    <section class="section-block"><div class="section-head"><h2>Completed games</h2><span class="pill">{filteredCompleted.length} results</span></div>{#if filteredCompleted.length}<div class="game-grid">{#each filteredCompleted as game (game.appid)}<GameCard {game} {gameTags} {tagLabel} {toneClass} {tagTone} {gameTitle} {gameWarning} {hasAchievements} {shouldShowSyncErrorMessage} {cardProgress} {completionLabel} {formatPlaytime} {pct} {missingAvgUnlockLabel} onContextMenu={onOpenGameMenu} />{/each}</div>{:else}<p class="empty-state">{viewSearch ? 'No matching games.' : 'No completed games found yet.'}</p>{/if}</section>
  {:else if activeView === 'warnings'}
    <SyncErrorsView games={filteredWarnings} {viewSearch} {tagLabel} {toneClass} {tagTone} {gameTitle} {gameWarning} {hasAchievements} {shouldShowSyncErrorMessage} {completionLabel} {pct} {gameActionLoading} {onOpenGameMenu} onRunGameAction={onRunGameAction} />
  {:else if activeView === 'disabled'}
    <DisabledView games={filteredDisabled} {viewSearch} {gameTitle} {gameWarning} {hasAchievements} {completionLabel} {pct} {formatPlaytime} {onRunGameAction} showDisabledReasonLabel={showDisabledReasonLabel} />
  {:else}
    <SettingsView {state} {clearingData} onClearData={onClearData} />
  {/if}
</main>
