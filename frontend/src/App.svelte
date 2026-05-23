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
    SyncNow,
    ToggleGameMissingAchievementsInDLC,
    ValidateProfile
  } from '../wailsjs/go/main/App.js'
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
  import {gameTags, suggestionTagOptions, tagLabel, tagTone, toneClass, tagCountFromGames} from './lib/tag.js'
  import {syncDetails, syncTitle} from './lib/sync.js'
  import {showDisabledReasonLabel} from './lib/disabled.js'

  let state = null
  let loading = true
  let error = ''
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
      error = ''
      const nextState = await GetAppState()
      if (token === stateRequestToken) state = nextState
    } catch (err) {
      if (token === stateRequestToken) error = formatMessage(err)
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
      const nextState = await RetryAPIKey()
      if (token === stateRequestToken) {
        state = nextState
        schedulePolling()
      }
    } catch (err) {
      if (token === stateRequestToken) error = formatMessage(err)
    }
  }

  async function validateProfile() {
    const input = profileInput.trim()
    if (!input) return
    validating = true
    preview = null
    error = ''
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
    try {
      const nextState = await SaveProfile(preview)
      if (token === stateRequestToken) {
        state = nextState
        schedulePolling()
      }
    } catch (err) {
      if (token === stateRequestToken) error = formatMessage(err)
    } finally {
      saving = false
    }
  }

  async function syncNow() {
    const token = ++stateRequestToken
    error = ''
    try {
      const nextState = await SyncNow()
      if (token === stateRequestToken) {
        state = nextState
        schedulePolling()
      }
    } catch (err) {
      if (token === stateRequestToken) error = formatMessage(err)
    }
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
    try {
      stopPolling()
      const nextState = await ClearActiveUserData()
      if (token === stateRequestToken) {
        state = nextState
        activeView = 'suggestions'
        profileInput = ''
        preview = null
        clearConfirmOpen = false
        clearConfirmInput = ''
        schedulePolling()
      }
    } catch (err) {
      if (token === stateRequestToken) error = formatMessage(err)
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
    try {
      let nextState
      if (action === 'previously-completed') nextState = await MarkGamePreviouslyCompleted(game.appid)
      else if (action === 'toggle-dlc') nextState = await ToggleGameMissingAchievementsInDLC(game.appid)
      else if (action === 'disable') nextState = await DisableGame(game.appid)
      else if (action === 'enable') nextState = await EnableGame(game.appid)
      if (nextState && token === stateRequestToken) {
        state = nextState
        schedulePolling()
      }
    } catch (err) {
      if (token === stateRequestToken) error = formatMessage(err)
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
  $: suggestionTagCounts = Object.fromEntries(suggestionTagOptions.map((tag) => [tag, tagCountFromGames(suggestions, tag)]))
  $: visibleSuggestionTagOptions = suggestionTagOptions.filter((tag) => suggestionTagCounts[tag] > 0)
  $: if (activeView !== 'suggestions' || (activeTagFilter && tagCountFromGames(suggestions, activeTagFilter) === 0)) activeTagFilter = ''
  $: syncCardTone = syncIsRunning ? 'info' : latestSyncRun?.status === 'partial' || latestSyncRun?.status === 'failed' ? 'warn' : ''
  $: filteredSuggestions = suggestions.filter((game) => matchesViewFilters(game, normalizedViewSearch, activeTagFilter))
  $: filteredCompleted = completed.filter((game) => matchesSearchQuery(game, normalizedViewSearch))
  $: filteredWarnings = warningGames.filter((game) => matchesSearchQuery(game, normalizedViewSearch))
  $: filteredDisabled = disabled.filter((game) => matchesSearchQuery(game, normalizedViewSearch))
  $: suggestionsFiltered = Boolean(viewSearch.trim() || activeTagFilter)
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
    {syncCardTone}
    {activeView}
    {viewSearch}
    {activeTagFilter}
    bind:viewSearchInput
    {suggestions}
    {suggestionTagCounts}
    {visibleSuggestionTagOptions}
    {filteredSuggestions}
    {filteredCompleted}
    {filteredWarnings}
    {filteredDisabled}
    {suggestionsFiltered}
    {gameActionLoading}
    {clearingData}
    onSyncNow={syncNow}
    onActiveViewChange={(value) => (activeView = value)}
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
    syncTitle={syncTitle}
    syncDetails={syncDetails}
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
      <button on:click={() => runGameAction('enable')} disabled={gameActionLoading}>Re-enable game</button>
    {:else if !contextMenu.game.disabled}
      <button class="danger" on:click={() => runGameAction('disable')} disabled={gameActionLoading}>Disable game</button>
    {/if}
  </div>
{/if}
