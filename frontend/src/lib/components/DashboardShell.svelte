<script>
  import SummaryStats from './SummaryStats.svelte'
  import ProfileCard from './ProfileCard.svelte'
  import GameCard from './GameCard.svelte'
  import IncrementalGrid from './IncrementalGrid.svelte'
  import SyncErrorsView from './SyncErrorsView.svelte'
  import DisabledView from './DisabledView.svelte'
  import SettingsView from './SettingsView.svelte'
  import UpdateBanner from './UpdateBanner.svelte'
  import {gameTags} from '../tag.js'
  let {
    state,
    // updates: the update store (lib/update.js). Optional; without it no update UI shows.
    updates = null,
    error = '',
    summary = {},
    latestSyncRun = null,
    syncIsRunning = false,
    activeView = 'suggestions',
    // One-way from App. The clear button sets it locally and tells App through onViewSearch; App's next value replaces it.
    viewSearch = '',
    activeTagFilter = '',
    // Two-way with App (bind:viewSearchInput): set by bind:this on the search input.
    viewSearchInput = $bindable(null),
    tagFilterGames = [],
    tagFilterCounts = {},
    visibleTagFilterOptions = [],
    filteredSuggestions = [],
    filteredCompleted = [],
    filteredWarnings = [],
    filteredDisabled = [],
    viewFiltered = false,
    gameActionLoading = false,
    clearingData = false,
    savingSettings = false,
    onSyncNow = () => {},
    onSetLibrarySettings = () => {},
    onViewSearch = () => {},
    onActiveTagFilter = () => {},
    onOpenGameMenu = () => {},
    onRunGameAction = () => {},
    onClearData = () => {},
    gameTitle,
    gameWarning,
    formatPlaytime,
    toneClass,
    tagTone,
    tagLabel,
    integer,
    pct,
    cardProgress,
    completionLabel,
    missingAvgUnlockLabel,
    hasAchievements,
    shouldShowSyncErrorMessage,
    showDisabledReasonLabel
  } = $props()

  let tagFilterVisible = $derived((activeView === 'suggestions' || activeView === 'completed') && visibleTagFilterOptions.length > 0)
  let tagFilterLabel = $derived(activeView === 'completed' ? 'Filter completed games by tag' : 'Filter not completed games by tag')
  // Search text is trimmed so a trailing space does not reset the incremental grid.
  let listResetKey = $derived(`${viewSearch.trim()}|${activeTagFilter}`)

  // Own clear button instead of the native search cancel button (hidden in CSS): the native one is a tiny background
  // image that rasterises unevenly on WebKitGTK.
  function clearSearch() {
    viewSearch = ''
    onViewSearch('')
    if (viewSearchInput) viewSearchInput.focus()
  }
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
  <UpdateBanner {updates} />
  {#if error}<div class="banner error-banner" aria-live="polite">{error}</div>{/if}
  {#if activeView !== 'settings'}
    <section class="view-toolbar" aria-label="Search current view"><div class="search-field"><input bind:this={viewSearchInput} class="search-input" type="search" placeholder="Search games in this view" bind:value={viewSearch} oninput={(e) => onViewSearch(e.currentTarget.value)} />{#if viewSearch}<button type="button" class="search-clear" aria-label="Clear search" onclick={clearSearch}><svg viewBox="0 0 16 16" width="16" height="16" aria-hidden="true" focusable="false"><path d="M4 4L12 12M12 4L4 12" /></svg></button>{/if}</div>{#if tagFilterVisible}<div class="tag-filter-panel" aria-label={tagFilterLabel}><div class="tag-filter-heading"><span>Filter by tag</span>{#if activeTagFilter}<button class="link-button" onclick={() => onActiveTagFilter('')}>Clear tag filter</button>{/if}</div><div class="tag-filter-list"><button class="tag tag-filter" class:active={!activeTagFilter} aria-pressed={!activeTagFilter} onclick={() => onActiveTagFilter('')}>All <span>{integer(tagFilterGames.length)}</span></button>{#each visibleTagFilterOptions as tag}<button class={`tag tag-filter ${toneClass(tagTone(tag))}`} class:active={activeTagFilter === tag} aria-pressed={activeTagFilter === tag} onclick={() => onActiveTagFilter(tag)}>{tagLabel(tag)} <span>{integer(tagFilterCounts[tag])}</span></button>{/each}</div></div>{/if}</section>
  {/if}
  {#if activeView === 'suggestions'}
    {#if filteredSuggestions.length}<IncrementalGrid items={filteredSuggestions} resetKey={listResetKey} gridClass="game-grid incomplete-grid">{#snippet children(game)}<GameCard {game} {gameTags} {tagLabel} {toneClass} {tagTone} {gameTitle} {gameWarning} {hasAchievements} {shouldShowSyncErrorMessage} {cardProgress} {completionLabel} {formatPlaytime} {pct} {missingAvgUnlockLabel} onContextMenu={onOpenGameMenu} />{/snippet}</IncrementalGrid>{:else}<p class="empty-state">{viewFiltered ? 'No games match the current filters.' : 'No incomplete games found yet.'}</p>{/if}
  {:else if activeView === 'completed'}
    <section class="section-block"><div class="section-head"><h2>Completed games</h2><span class="pill">{filteredCompleted.length} results</span></div>{#if filteredCompleted.length}<IncrementalGrid items={filteredCompleted} resetKey={listResetKey} gridClass="game-grid">{#snippet children(game)}<GameCard {game} {gameTags} {tagLabel} {toneClass} {tagTone} {gameTitle} {gameWarning} {hasAchievements} {shouldShowSyncErrorMessage} {cardProgress} {completionLabel} {formatPlaytime} {pct} {missingAvgUnlockLabel} onContextMenu={onOpenGameMenu} />{/snippet}</IncrementalGrid>{:else}<p class="empty-state">{viewFiltered ? 'No matching games.' : 'No completed games found yet.'}</p>{/if}</section>
  {:else if activeView === 'warnings'}
    <SyncErrorsView games={filteredWarnings} {viewSearch} {tagLabel} {toneClass} {tagTone} {gameTitle} {gameWarning} {hasAchievements} {shouldShowSyncErrorMessage} {completionLabel} {pct} {gameActionLoading} {onOpenGameMenu} onRunGameAction={onRunGameAction} />
  {:else if activeView === 'disabled'}
    <DisabledView games={filteredDisabled} {viewSearch} {gameTitle} {gameWarning} {hasAchievements} {completionLabel} {pct} {formatPlaytime} {gameActionLoading} {onRunGameAction} showDisabledReasonLabel={showDisabledReasonLabel} />
  {:else}
    <SettingsView {state} {updates} {clearingData} {savingSettings} onClearData={onClearData} onSetLibrarySettings={onSetLibrarySettings} />
  {/if}
</main>
