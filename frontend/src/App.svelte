<script>
  import {onMount, tick} from 'svelte'
  import {
    ClearActiveUserData,
    DisableGame,
    EnableGame,
    GetAppState,
    MarkGamePreviouslyCompleted,
    RetryAPIKey,
    SaveProfile,
    SetLibrarySettings,
    SyncNow,
    ToggleGameMissingAchievementsInDLC,
    ValidateProfile
  } from '../bindings/github.com/gjrud/steam-achievement-tracker/app.js'
  import SetupView from './lib/components/SetupView.svelte'
  import DashboardShell from './lib/components/DashboardShell.svelte'
  import ClearDataConfirmModal from './lib/components/ClearDataConfirmModal.svelte'
  import {formatMessage, formatPlaytime, integer, pct} from './lib/format.js'
  import {
    cardProgress,
    completionLabel,
    gameTitle,
    gameWarning,
    hasAchievements,
    missingAvgUnlockLabel,
    shouldShowSyncErrorMessage
  } from './lib/game.js'
  import {matchesViewFilters, matchesSearchQuery, sortByTitle} from './lib/filter.js'
  import {gameTags, suggestionTagOptions, tagLabel, tagTone, toneClass, tagCountsFromGames} from './lib/tag.js'
  import {showDisabledReasonLabel} from './lib/disabled.js'

  let state = null
  let loading = true
  let error = ''
  // pollError is true only while `error` holds a background poll failure. Polls clear only that, so an action's
  // error survives the polls that keep running while a sync is in progress.
  let pollError = false
  let profileInput = ''
  let preview = null
  let validating = false
  let saving = false
  let activeView = 'suggestions'
  let viewSearch = ''
  let activeTagFilter = ''
  let viewSearchInput = null
  let pollHandle = null
  let mounted = false
  let stateRequestToken = 0
  let contextMenu = null
  let contextMenuEl = null
  let gameActionLoading = false
  let clearingData = false
  let savingSettings = false
  let clearConfirmOpen = false
  let clearConfirmInput = ''
  const menuMargin = 12

  onMount(() => {
    mounted = true
    loadState({reschedule: true})
    return () => {
      mounted = false
      stopPolling()
    }
  })

  async function loadState(options = {}) {
    const token = ++stateRequestToken
    try {
      if (pollError) {
        error = ''
        pollError = false
      }
      const nextState = await GetAppState()
      if (token === stateRequestToken) state = nextState
    } catch (err) {
      if (token === stateRequestToken) {
        error = formatMessage(err)
        pollError = true
      }
    } finally {
      if (token === stateRequestToken) loading = false
      if (options.reschedule && token === stateRequestToken) schedulePolling()
    }
  }

  function schedulePolling() {
    stopPolling()
    if (!mounted) return
    const syncing = Boolean(state?.syncInProgress || state?.dashboard?.latestSyncRun?.status === 'running')
    if (!syncing) return
    pollHandle = setTimeout(() => loadState({reschedule: true}), 1000)
  }

  async function retryKey() {
    const token = ++stateRequestToken
    try {
      error = ''
      pollError = false
      const nextState = await RetryAPIKey()
      if (token === stateRequestToken) {
        state = nextState
        schedulePolling()
      }
    } catch (err) {
      if (token === stateRequestToken) {
        error = formatMessage(err)
        schedulePolling()
      }
    }
  }

  async function validateProfile() {
    const input = profileInput.trim()
    if (!input) return
    validating = true
    preview = null
    error = ''
    pollError = false
    try {
      preview = await ValidateProfile(input)
    } catch (err) {
      error = formatMessage(err)
    } finally {
      validating = false
    }
  }

  async function saveProfile() {
    if (!preview) return
    const token = ++stateRequestToken
    saving = true
    error = ''
    pollError = false
    try {
      const nextState = await SaveProfile(preview)
      if (token === stateRequestToken) {
        state = nextState
        schedulePolling()
      }
    } catch (err) {
      if (token === stateRequestToken) {
        error = formatMessage(err)
        schedulePolling()
      }
    } finally {
      saving = false
    }
  }

  async function syncNow() {
    const token = ++stateRequestToken
    error = ''
    pollError = false
    try {
      const nextState = await SyncNow()
      if (token === stateRequestToken) {
        state = nextState
        schedulePolling()
      }
    } catch (err) {
      if (token === stateRequestToken) {
        error = formatMessage(err)
        schedulePolling()
      }
    }
  }

  // Settings only change what the next sync gathers; this does not start a sync.
  async function saveLibrarySettings(includeUnvettedApps, includePlayedFreeGames) {
    if (savingSettings) return
    // Stop the sync poll first: a tick during the save would bump stateRequestToken and drop this result.
    // schedulePolling() below restarts it if the sync is still running.
    stopPolling()
    const token = ++stateRequestToken
    savingSettings = true
    error = ''
    pollError = false
    try {
      const nextState = await SetLibrarySettings(includeUnvettedApps, includePlayedFreeGames)
      if (token === stateRequestToken) {
        state = nextState
        schedulePolling()
      }
    } catch (err) {
      if (token === stateRequestToken) {
        error = formatMessage(err)
        schedulePolling()
      }
    } finally {
      savingSettings = false
    }
  }

  function changeView(value) {
    if (value === activeView) return
    activeView = value
    activeTagFilter = ''
  }

  function requestClearActiveUserData() {
    if (clearingData || !state?.profile) return
    clearConfirmInput = ''
    clearConfirmOpen = true
  }

  function closeClearConfirm() {
    if (clearingData) return
    clearConfirmOpen = false
    clearConfirmInput = ''
  }

  async function confirmClearActiveUserData() {
    if (clearingData || !state?.profile) return
    if (clearConfirmInput !== clearConfirmText) return
    const token = ++stateRequestToken
    clearingData = true
    error = ''
    pollError = false
    try {
      stopPolling()
      const nextState = await ClearActiveUserData()
      if (token === stateRequestToken) {
        state = nextState
        activeView = 'suggestions'
        activeTagFilter = ''
        profileInput = ''
        preview = null
        clearConfirmOpen = false
        clearConfirmInput = ''
        schedulePolling()
      }
    } catch (err) {
      if (token === stateRequestToken) {
        error = formatMessage(err)
        // stopPolling() ran before the call; restart it if the app is still syncing.
        schedulePolling()
      }
    } finally {
      clearingData = false
    }
  }

  async function openGameMenu(event, game) {
    contextMenu = {game, x: event.clientX, y: event.clientY, left: event.clientX, top: event.clientY, ready: false}
    await tick()
    positionGameMenu()
  }

  function closeGameMenu() {
    if (!gameActionLoading) contextMenu = null
  }

  function handleGlobalKeydown(event) {
    if (event.key === 'Escape') {
      closeGameMenu()
      closeClearConfirm()
    }
    if (!shouldFocusSearch(event)) return
    if (!viewSearchInput) return
    event.preventDefault()
    viewSearchInput.focus()
    viewSearch = `${viewSearch}${event.key}`
  }

  function shouldFocusSearch(event) {
    if (!['suggestions', 'completed', 'warnings', 'disabled'].includes(activeView)) return false
    if (event.metaKey || event.ctrlKey || event.altKey) return false
    if (event.key.length !== 1) return false
    return !isEditableTarget(event.target) && !isInteractiveTarget(event.target)
  }

  function isEditableTarget(target) {
    return target instanceof HTMLElement && (target.isContentEditable || ['INPUT', 'TEXTAREA', 'SELECT'].includes(target.tagName))
  }

  function isInteractiveTarget(target) {
    return target instanceof HTMLElement && Boolean(target.closest('button, a, [role="button"]'))
  }

  function positionGameMenu() {
    if (!contextMenu || !contextMenuEl) return
    const rect = contextMenuEl.getBoundingClientRect()
    const left = Math.max(menuMargin, Math.min(contextMenu.x, window.innerWidth - rect.width - menuMargin))
    const top = Math.max(menuMargin, Math.min(contextMenu.y, window.innerHeight - rect.height - menuMargin))
    contextMenu = {...contextMenu, left, top, ready: true}
  }

  async function runGameAction(action, selectedGame = contextMenu?.game) {
    if (!selectedGame || gameActionLoading) return
    const game = selectedGame
    const token = ++stateRequestToken
    gameActionLoading = true
    error = ''
    pollError = false
    try {
      let nextState
      if (action === 'previously-completed') nextState = await MarkGamePreviouslyCompleted(game.appid, game.groupId)
      else if (action === 'toggle-dlc') nextState = await ToggleGameMissingAchievementsInDLC(game.appid, game.groupId)
      else if (action === 'disable') nextState = await DisableGame(game.appid, game.groupId)
      else if (action === 'enable') nextState = await EnableGame(game.appid, game.groupId)
      if (nextState && token === stateRequestToken) {
        state = nextState
        schedulePolling()
      }
    } catch (err) {
      if (token === stateRequestToken) {
        error = formatMessage(err)
        schedulePolling()
      }
    } finally {
      gameActionLoading = false
      contextMenu = null
    }
  }

  function stopPolling() {
    if (pollHandle) clearTimeout(pollHandle)
    pollHandle = null
  }

  $: dashboard = state?.dashboard || null
  $: latestSyncRun = dashboard?.latestSyncRun || null
  $: syncIsRunning = Boolean(state?.syncInProgress || latestSyncRun?.status === 'running')
  $: summary = dashboard?.summary || {}
  $: suggestions = dashboard?.suggestions || []
  $: completed = sortByTitle(dashboard?.completed || [])
  $: disabled = sortByTitle(dashboard?.disabled || [])
  $: normalizedViewSearch = viewSearch.trim().toLowerCase()
  $: warningGames = (dashboard?.warnings || []).filter((game) => game.syncWarning)
  // Tag filter chips follow the active view's list; views without a tag filter get no games and no chips.
  $: tagFilterGames = activeView === 'completed' ? completed : activeView === 'suggestions' ? suggestions : []
  $: tagFilterCounts = tagCountsFromGames(tagFilterGames)
  $: visibleTagFilterOptions = suggestionTagOptions.filter((tag) => tagFilterCounts[tag] > 0)
  $: if (activeTagFilter && !tagFilterCounts[activeTagFilter]) activeTagFilter = ''
  $: filteredSuggestions = suggestions.filter((game) => matchesViewFilters(game, normalizedViewSearch, activeTagFilter))
  $: filteredCompleted = completed.filter((game) => matchesViewFilters(game, normalizedViewSearch, activeTagFilter))
  $: filteredWarnings = warningGames.filter((game) => matchesSearchQuery(game, normalizedViewSearch))
  $: filteredDisabled = disabled.filter((game) => matchesSearchQuery(game, normalizedViewSearch))
  $: viewFiltered = Boolean(viewSearch.trim() || activeTagFilter)
  $: clearConfirmText = state?.profile ? state.profile.steamId64 || state.profile.displayName : ''
</script>

<svelte:window on:click={closeGameMenu} on:keydown={handleGlobalKeydown} />

{#if loading}
  <main class="loading-screen">
    <div class="loader"></div>
    <p>Loading tracker…</p>
  </main>
{:else if state?.initError}
  <SetupView mode="startup-error" state={state} error={error} onRetry={retryKey} />
{:else if !state?.apiKeyPresent}
  <SetupView mode="missing-key" state={state} error={error} onRetry={retryKey} />
{:else if !state?.profileExists}
  <SetupView
    mode="profile"
    state={state}
    error={error}
    profileInput={profileInput}
    preview={preview}
    validating={validating}
    saving={saving}
    onValidate={validateProfile}
    onSave={saveProfile}
    onProfileInput={(value) => (profileInput = value)}
  />
{:else}
  <DashboardShell
    {state}
    {error}
    {summary}
    {latestSyncRun}
    {syncIsRunning}
    {activeView}
    {viewSearch}
    {activeTagFilter}
    bind:viewSearchInput
    {tagFilterGames}
    {tagFilterCounts}
    {visibleTagFilterOptions}
    {filteredSuggestions}
    {filteredCompleted}
    {filteredWarnings}
    {filteredDisabled}
    {viewFiltered}
    {gameActionLoading}
    {clearingData}
    {savingSettings}
    onSyncNow={syncNow}
    onSetLibrarySettings={saveLibrarySettings}
    onActiveViewChange={changeView}
    onViewSearch={(value) => (viewSearch = value)}
    onActiveTagFilter={(value) => (activeTagFilter = value)}
    onOpenGameMenu={openGameMenu}
    onRunGameAction={runGameAction}
    onClearData={requestClearActiveUserData}
    tagLabel={tagLabel}
    tagTone={tagTone}
    toneClass={toneClass}
    gameTitle={gameTitle}
    gameWarning={gameWarning}
    formatPlaytime={formatPlaytime}
    integer={integer}
    pct={pct}
    cardProgress={cardProgress}
    completionLabel={completionLabel}
    missingAvgUnlockLabel={missingAvgUnlockLabel}
    hasAchievements={hasAchievements}
    shouldShowSyncErrorMessage={shouldShowSyncErrorMessage}
    showDisabledReasonLabel={showDisabledReasonLabel}
  />
{/if}

{#if clearConfirmOpen}
  <ClearDataConfirmModal
    confirmText={clearConfirmText}
    bind:inputValue={clearConfirmInput}
    clearing={clearingData}
    onCancel={closeClearConfirm}
    onConfirm={confirmClearActiveUserData}
  />
{/if}

{#if contextMenu}
  <div bind:this={contextMenuEl} class="context-menu" role="menu" tabindex="-1" style={`left: ${contextMenu.left}px; top: ${contextMenu.top}px; visibility: ${contextMenu.ready ? 'visible' : 'hidden'};`} on:click|stopPropagation on:keydown|stopPropagation={(event) => event.key === 'Escape' && closeGameMenu()}>
    <p>{gameTitle(contextMenu.game)}</p>
    {#if activeView === 'suggestions'}
      <button on:click={() => runGameAction('previously-completed')} disabled={gameActionLoading}>{contextMenu.game.manualWasCompleted ? 'Clear previously completed flag' : 'Mark as previously completed'}</button>
      <button on:click={() => runGameAction('toggle-dlc')} disabled={gameActionLoading}>{contextMenu.game.missingAchievementsInDLC ? 'Clear DLC-missing flag' : 'Flag missing achievements in DLC'}</button>
    {/if}
    {#if contextMenu.game.disabled}
      <button on:click={() => runGameAction('enable')} disabled={gameActionLoading}>{contextMenu.game.isSet ? 'Re-enable set' : 'Re-enable game'}</button>
    {:else}
      <button class="danger" on:click={() => runGameAction('disable')} disabled={gameActionLoading}>{contextMenu.game.isSet ? 'Disable set' : 'Disable game'}</button>
    {/if}
  </div>
{/if}
