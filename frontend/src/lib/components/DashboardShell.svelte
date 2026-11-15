<script>
  import SummaryStats from './SummaryStats.svelte'
  import ProfileCard from './ProfileCard.svelte'
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
  export let activeView = 'suggestions'
  export let viewSearch = ''
  export let activeTagFilter = ''
  export let viewSearchInput = null
  export let tagFilterGames = []
  export let tagFilterCounts = {}
  export let visibleTagFilterOptions = []
  export let filteredSuggestions = []
  export let filteredCompleted = []
  export let filteredWarnings = []
  export let filteredDisabled = []
  export let viewFiltered = false
  export let gameActionLoading = false
  export let clearingData = false
  export let savingSettings = false
  export let onSyncNow = () => {}
  export let onSetLibrarySettings = () => {}
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

  $: tagFilterVisible = (activeView === 'suggestions' || activeView === 'completed') && visibleTagFilterOptions.length > 0
  $: tagFilterLabel = activeView === 'completed' ? 'Filter completed games by tag' : 'Filter not completed games by tag'
</script>

<main class="app-shell">
  <header class="dashboard-header">
    <div class="brand">
      <p class="eyebrow brand-title">Steam Achievement Tracker</p>
      <h1 class="sr-only">Dashboard</h1>
      <p class="brand-subtitle">Spot new achievements without the hunt.</p>
    </div>
    <SummaryStats {summary} {integer} />
    <ProfileCard {state} {latestSyncRun} {syncIsRunning} {onSyncNow} />
  </header>
  {#if error}<div class="banner error-banner" aria-live="polite">{error}</div>{/if}
  <nav class="tabs" aria-label="Dashboard views"><button class="tab" class:active={activeView === 'suggestions'} aria-current={activeView === 'suggestions' ? 'page' : undefined} on:click={() => onActiveViewChange('suggestions')}>Not completed</button><button class="tab" class:active={activeView === 'completed'} aria-current={activeView === 'completed' ? 'page' : undefined} on:click={() => onActiveViewChange('completed')}>Completed</button><button class="tab" class:active={activeView === 'warnings'} aria-current={activeView === 'warnings' ? 'page' : undefined} on:click={() => onActiveViewChange('warnings')}>Sync errors</button><button class="tab" class:active={activeView === 'disabled'} aria-current={activeView === 'disabled' ? 'page' : undefined} on:click={() => onActiveViewChange('disabled')}>Disabled</button><button class="tab tab-settings" class:active={activeView === 'settings'} aria-current={activeView === 'settings' ? 'page' : undefined} on:click={() => onActiveViewChange('settings')}>Settings</button></nav>
  {#if activeView !== 'settings'}
    <section class="view-toolbar" aria-label="Search current view"><input bind:this={viewSearchInput} class="search-input" type="search" placeholder="Search games in this view" bind:value={viewSearch} on:input={(e) => onViewSearch(e.currentTarget.value)} />{#if tagFilterVisible}<div class="tag-filter-panel" aria-label={tagFilterLabel}><div class="tag-filter-heading"><span>Filter by tag</span>{#if activeTagFilter}<button class="link-button" on:click={() => onActiveTagFilter('')}>Clear tag filter</button>{/if}</div><div class="tag-filter-list"><button class="tag tag-filter" class:active={!activeTagFilter} aria-pressed={!activeTagFilter} on:click={() => onActiveTagFilter('')}>All <span>{integer(tagFilterGames.length)}</span></button>{#each visibleTagFilterOptions as tag}<button class={`tag tag-filter ${toneClass(tagTone(tag))}`} class:active={activeTagFilter === tag} aria-pressed={activeTagFilter === tag} on:click={() => onActiveTagFilter(tag)}>{tagLabel(tag)} <span>{integer(tagFilterCounts[tag])}</span></button>{/each}</div></div>{/if}</section>
  {/if}
  {#if activeView === 'suggestions'}
    {#if filteredSuggestions.length}<div class="game-grid incomplete-grid">{#each filteredSuggestions as game (game.entryKey)}<GameCard {game} {gameTags} {tagLabel} {toneClass} {tagTone} {gameTitle} {gameWarning} {hasAchievements} {shouldShowSyncErrorMessage} {cardProgress} {completionLabel} {formatPlaytime} {pct} {missingAvgUnlockLabel} onContextMenu={onOpenGameMenu} />{/each}</div>{:else}<p class="empty-state">{viewFiltered ? 'No games match the current filters.' : 'No incomplete games found yet.'}</p>{/if}
  {:else if activeView === 'completed'}
    <section class="section-block"><div class="section-head"><h2>Completed games</h2><span class="pill">{filteredCompleted.length} results</span></div>{#if filteredCompleted.length}<div class="game-grid">{#each filteredCompleted as game (game.entryKey)}<GameCard {game} {gameTags} {tagLabel} {toneClass} {tagTone} {gameTitle} {gameWarning} {hasAchievements} {shouldShowSyncErrorMessage} {cardProgress} {completionLabel} {formatPlaytime} {pct} {missingAvgUnlockLabel} onContextMenu={onOpenGameMenu} />{/each}</div>{:else}<p class="empty-state">{viewFiltered ? 'No matching games.' : 'No completed games found yet.'}</p>{/if}</section>
  {:else if activeView === 'warnings'}
    <SyncErrorsView games={filteredWarnings} {viewSearch} {tagLabel} {toneClass} {tagTone} {gameTitle} {gameWarning} {hasAchievements} {shouldShowSyncErrorMessage} {completionLabel} {pct} {gameActionLoading} {onOpenGameMenu} onRunGameAction={onRunGameAction} />
  {:else if activeView === 'disabled'}
    <DisabledView games={filteredDisabled} {viewSearch} {gameTitle} {gameWarning} {hasAchievements} {completionLabel} {pct} {formatPlaytime} {onRunGameAction} showDisabledReasonLabel={showDisabledReasonLabel} />
  {:else}
    <SettingsView {state} {clearingData} {savingSettings} onClearData={onClearData} onSetLibrarySettings={onSetLibrarySettings} />
  {/if}
</main>
