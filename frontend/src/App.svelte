<script>
  import {onMount, tick} from 'svelte'
  import {Events} from '@wailsio/runtime'
  import {
    ClearActiveUserData,
    DisableGame,
    EnableGame,
    GetAppState,
    MarkGamePreviouslyCompleted,
    RetryAPIKey,
    SaveAPIKey,
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
  // refreshError is true only while `error` holds a failed state refresh. Refreshes clear only that, so an action's
  // error survives the refreshes that run while a sync is in progress.
  let refreshError = false
  let profileInput = ''
  let preview = null
  let validating = false
  let saving = false
  // savingKey covers a SaveAPIKey call. It lives here (not in the form) so a remounted form still sees it. It is not
  // a beginAction: the unlock prompt can stay open for a long time, and holding reads would freeze live sync progress.
  let savingKey = false
  let activeView = 'suggestions'
  let viewSearch = ''
  let activeTagFilter = ''
  let viewSearchInput = null
  let unsubscribeSyncEvents = null
  let retryHandle = null
  // refreshing: a GetAppState read is in flight. refreshQueued: a fresh read is owed; events set it, so a burst
  // collapses into one read. busyActions: actions in flight; reads wait for them so their results land in order.
  let refreshing = false
  let refreshQueued = false
  let busyActions = 0
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
  // Wails event the Go backend emits (no payload) on sync start, after each game's progress write, after the final
  // status write, and once more after the sync flag clears.
  const syncChangedEvent = 'sync:changed'
  const refreshRetryDelayMs = 2000

  onMount(() => {
    mounted = true
    // Subscribe before the first read so a sync:changed emitted during that read is not missed.
    unsubscribeSyncEvents = Events.On(syncChangedEvent, requestRefresh)
    requestRefresh()
    return () => {
      mounted = false
      unsubscribeSyncEvents?.()
      unsubscribeSyncEvents = null
      clearRetry()
    }
  })

  // requestRefresh asks for a fresh GetAppState read. Reads never overlap and never run during an action; requests
  // that arrive meanwhile collapse into a single read that runs once the current work finishes.
  function requestRefresh() {
    if (!mounted) return
    clearRetry()
    refreshQueued = true
    drainRefreshes()
  }

  function drainRefreshes() {
    if (!mounted || refreshing || busyActions > 0 || !refreshQueued) return
    refreshQueued = false
    refresh()
  }

  // refresh reads the app state once. A read that an action overtook is dropped and re-queued, so the state shown
  // always comes from the newest read that finished.
  async function refresh() {
    refreshing = true
    const token = ++stateRequestToken
    let retry = false
    try {
      const nextState = await GetAppState()
      if (token !== stateRequestToken) {
        refreshQueued = true
        return
      }
      state = nextState
      clearRetry()
      if (refreshError) {
        error = ''
        refreshError = false
      }
    } catch (err) {
      if (token !== stateRequestToken) {
        refreshQueued = true
        return
      }
      error = formatMessage(err)
      refreshError = true
      // A failed read while a sync runs would otherwise leave the button on Syncing… with no event left to fix it.
      retry = isSyncing(state)
    } finally {
      refreshing = false
      if (token === stateRequestToken) loading = false
      if (retry) scheduleRetry()
      drainRefreshes()
    }
  }

  // scheduleRetry arms one timer. If the retried read also fails while the sync still runs, it arms the next one.
  function scheduleRetry() {
    clearRetry()
    if (!mounted) return
    retryHandle = setTimeout(() => {
      retryHandle = null
      requestRefresh()
    }, refreshRetryDelayMs)
  }

  function clearRetry() {
    if (retryHandle) clearTimeout(retryHandle)
    retryHandle = null
  }

  // beginAction takes over the request token (so an in-flight read is dropped), clears the banner, and holds reads
  // until endAction. Every action must pair beginAction with endAction in finally. A pending failed-read retry is left
  // armed: if the action fails, that retry still recovers the sync view.
  function beginAction() {
    busyActions++
    error = ''
    refreshError = false
    return ++stateRequestToken
  }

  // applyActionState stores an action's result unless a newer request took over. Applying fresh state supersedes a
  // failed read's pending retry, so the retry is cancelled. Returns whether the state was applied.
  function applyActionState(token, nextState) {
    if (token !== stateRequestToken) return false
    state = nextState
    clearRetry()
    return true
  }

  function endAction() {
    busyActions--
    drainRefreshes()
  }

  // isSyncing reports whether a dashboard state shows a sync in progress, by the backend flag or the latest run.
  function isSyncing(s) {
    return Boolean(s?.syncInProgress || s?.dashboard?.latestSyncRun?.status === 'running')
  }

  async function retryKey() {
    const token = beginAction()
    try {
      const nextState = await RetryAPIKey()
      applyActionState(token, nextState)
    } catch (err) {
      if (token === stateRequestToken) error = formatMessage(err)
    } finally {
      endAction()
    }
  }

  async function validateProfile() {
    const input = profileInput.trim()
    // The Enter key in the profile input calls this directly, so the save guard lives here too, not only on the button.
    if (!input || savingKey) return
    validating = true
    preview = null
    error = ''
    refreshError = false
    try {
      preview = await ValidateProfile(input)
    } catch (err) {
      error = formatMessage(err)
    } finally {
      validating = false
    }
  }

  async function saveProfile() {
    if (!preview || savingKey) return
    saving = true
    const token = beginAction()
    try {
      const nextState = await SaveProfile(preview)
      applyActionState(token, nextState)
    } catch (err) {
      if (token === stateRequestToken) error = formatMessage(err)
    } finally {
      saving = false
      endAction()
    }
  }

  // saveApiKey stores the key with no beginAction (see savingKey). On success the stored key changed, so the profile
  // preview (validated with the old key) is dropped, an earlier Validate error is cleared, and state is re-read. On
  // failure it rethrows: the form shows the error, and this banner stays untouched.
  async function saveApiKey(key) {
    if (savingKey) throw new Error('A save is already in progress')
    savingKey = true
    try {
      await SaveAPIKey(key)
      preview = null
      error = ''
      refreshError = false
      requestRefresh()
    } finally {
      savingKey = false
    }
  }

  async function syncNow() {
    const token = beginAction()
    try {
      const nextState = await SyncNow()
      applyActionState(token, nextState)
    } catch (err) {
      if (token === stateRequestToken) error = formatMessage(err)
    } finally {
      endAction()
    }
  }

  // Settings only change what the next sync gathers; this does not start a sync.
  async function saveLibrarySettings(includeUnvettedApps, includePlayedFreeGames) {
    if (savingSettings) return
    savingSettings = true
    const token = beginAction()
    try {
      const nextState = await SetLibrarySettings(includeUnvettedApps, includePlayedFreeGames)
      applyActionState(token, nextState)
    } catch (err) {
      if (token === stateRequestToken) error = formatMessage(err)
    } finally {
      savingSettings = false
      endAction()
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
    clearingData = true
    const token = beginAction()
    try {
      const nextState = await ClearActiveUserData()
      if (applyActionState(token, nextState)) {
        activeView = 'suggestions'
        activeTagFilter = ''
        profileInput = ''
        preview = null
        clearConfirmOpen = false
        clearConfirmInput = ''
      }
    } catch (err) {
      if (token === stateRequestToken) error = formatMessage(err)
    } finally {
      clearingData = false
      endAction()
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
    gameActionLoading = true
    const token = beginAction()
    try {
      let nextState
      if (action === 'previously-completed') nextState = await MarkGamePreviouslyCompleted(game.appid, game.groupId)
      else if (action === 'toggle-dlc') nextState = await ToggleGameMissingAchievementsInDLC(game.appid, game.groupId)
      else if (action === 'disable') nextState = await DisableGame(game.appid, game.groupId)
      else if (action === 'enable') nextState = await EnableGame(game.appid, game.groupId)
      if (nextState) applyActionState(token, nextState)
    } catch (err) {
      if (token === stateRequestToken) error = formatMessage(err)
    } finally {
      gameActionLoading = false
      contextMenu = null
      endAction()
    }
  }

  $: dashboard = state?.dashboard || null
  $: latestSyncRun = dashboard?.latestSyncRun || null
  $: syncIsRunning = isSyncing(state)
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
  <SetupView mode="missing-key" state={state} error={error} onRetry={retryKey} savingKey={savingKey} onSaveKey={saveApiKey} />
{:else if !state?.profileExists}
  <SetupView
    mode="profile"
    state={state}
    error={error}
    profileInput={profileInput}
    preview={preview}
    validating={validating}
    saving={saving}
    savingKey={savingKey}
    onValidate={validateProfile}
    onSave={saveProfile}
    onSaveKey={saveApiKey}
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
    {savingKey}
    onSyncNow={syncNow}
    onSaveKey={saveApiKey}
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
