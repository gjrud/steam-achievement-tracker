<script>
  import {onMount, tick} from 'svelte'
  import {Events} from '@wailsio/runtime'
  import {
    CancelUpdate,
    CheckForUpdates,
    ClearActiveUserData,
    DisableGame,
    EnableGame,
    GetAppState,
    GetUpdateState,
    InstallUpdate,
    MarkGamePreviouslyCompleted,
    RestartToUpdate,
    RetryAPIKey,
    SaveAPIKey,
    SaveProfile,
    SetLibrarySettings,
    SetUpdateChecksEnabled,
    SyncNow,
    ToggleGameMissingAchievementsInDLC,
    ValidateProfile
  } from '../bindings/github.com/gjrud/steam-achievement-tracker/app.js'
  import SetupView from './lib/components/SetupView.svelte'
  import DashboardShell from './lib/components/DashboardShell.svelte'
  import ClearDataConfirmModal from './lib/components/ClearDataConfirmModal.svelte'
  import TitleBar from './lib/components/TitleBar.svelte'
  import ScrollBar from './lib/components/ScrollBar.svelte'
  import {AppMessage, formatMessage, formatPlaytime, integer, pct} from './lib/format.js'
  import {isSyncAlreadyRunningError} from './lib/sync.js'
  import {createUpdateStore} from './lib/update.js'
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

  // Replaced whole, never mutated: $state.raw skips the deep proxy.
  let appState = $state.raw(null)
  let loading = $state(true)
  let error = $state('')
  // refreshError is true only while `error` holds a failed state refresh. Refreshes clear only that, so an action's
  // error survives the refreshes that run while a sync is in progress.
  let refreshError = false
  let profileInput = $state('')
  // Replaced whole and passed back to SaveProfile unchanged: $state.raw.
  let preview = $state.raw(null)
  let validating = $state(false)
  let saving = $state(false)
  // savingKey covers a SaveAPIKey call. It lives here so a remounted form still sees it. Not a beginAction: the key form
  // only shows in SetupView missing-key/profile modes when no sync is live, so there are no reads to hold, and a failed
  // save must leave the banner alone (the form shows its own error).
  let savingKey = $state(false)
  // validateToken is bumped when a Validate result is no longer wanted (the input or key changed). A result that
  // arrives with an older token is dropped.
  let validateToken = 0
  // syncStarting covers a SyncNow call from start to finish. It blocks a second call and shows the sync as running
  // before the backend's state does.
  let syncStarting = $state(false)
  // syncEventCooldown is the timer that spaces event-driven reads (see onSyncChanged). syncEventPending records an
  // event that arrived during the cooldown and is owed one read when it ends.
  let syncEventCooldown = null
  let syncEventPending = false
  let activeView = $state('suggestions')
  let viewSearch = $state('')
  let activeTagFilter = $state('')
  // DOM refs the template binds in a block or passes as props are $state(null) (non_reactive_update otherwise); elements are never proxied.
  let viewSearchInput = $state(null)
  let unsubscribeSyncEvents = null
  let retryHandle = null
  // refreshing: a GetAppState read is in flight. refreshQueued: a fresh read is owed; events set it, so a burst
  // collapses into one read. busyActions: actions in flight; reads wait for them so their results land in order.
  let refreshing = false
  let refreshQueued = false
  let busyActions = 0
  let mounted = false
  let stateRequestToken = 0
  // Replaced whole on each change; $state.raw keeps `game` the app's own object (no proxy copy, so identity checks hold).
  let contextMenu = $state.raw(null)
  let contextMenuEl = $state(null)
  let gameActionLoading = $state(false)
  let clearingData = $state(false)
  let savingSettings = $state(false)
  let clearConfirmOpen = $state(false)
  let clearConfirmInput = $state('')
  const menuMargin = 12
  // Payload-less Wails event from the Go backend: sync start, progress writes (at most one per 500 ms), the end of the run.
  const syncChangedEvent = 'sync:changed'
  // Timed from each requestRefresh call, not from when the read starts (a queued read can start later).
  const syncEventGapMs = 300
  const refreshRetryDelayMs = 2000
  // Update state is separate: it follows update:changed. The banner shows only on the dashboard.
  const updates = createUpdateStore({
    getState: () => GetUpdateState(),
    check: () => CheckForUpdates(),
    install: (version) => InstallUpdate(version),
    cancel: () => CancelUpdate(),
    restart: () => RestartToUpdate(),
    setChecksEnabled: (enabled) => SetUpdateChecksEnabled(enabled)
  })

  onMount(() => {
    mounted = true
    // Subscribe before the first read so a sync:changed emitted during that read is not missed.
    unsubscribeSyncEvents = Events.On(syncChangedEvent, onSyncChanged)
    requestRefresh()
    const stopUpdates = updates.start()
    return () => {
      stopUpdates()
      mounted = false
      unsubscribeSyncEvents?.()
      unsubscribeSyncEvents = null
      clearRetry()
      clearSyncEventCooldown()
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

  // sync:changed: read at once unless a read happened within syncEventGapMs; then read once when the gap ends.
  // Action-driven reads (requestRefresh) never wait.
  function onSyncChanged() {
    if (!mounted) return
    if (syncEventCooldown) {
      syncEventPending = true
      return
    }
    startSyncEventRead()
  }

  function startSyncEventRead() {
    syncEventPending = false
    requestRefresh()
    syncEventCooldown = setTimeout(() => {
      syncEventCooldown = null
      if (syncEventPending) startSyncEventRead()
    }, syncEventGapMs)
  }

  function clearSyncEventCooldown() {
    if (syncEventCooldown) clearTimeout(syncEventCooldown)
    syncEventCooldown = null
    syncEventPending = false
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
      appState = nextState
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
      retry = isSyncing(appState)
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

  // beginAction takes the request token (dropping any in-flight read), clears the banner and holds reads until
  // endAction, which must run in finally. A pending failed-read retry stays armed.
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
    appState = nextState
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

  // changeProfileInput stores the typed profile. A Validate still in flight is for the old text, so its result is
  // dropped.
  function changeProfileInput(value) {
    profileInput = value
    validateToken++
  }

  async function validateProfile() {
    const input = profileInput.trim()
    // The Enter key in the profile input calls this directly, so the guards live here too, not only on the button.
    if (!input || savingKey || validating) return
    const token = ++validateToken
    validating = true
    preview = null
    error = ''
    refreshError = false
    try {
      const result = await ValidateProfile(input)
      if (token === validateToken) preview = result
    } catch (err) {
      if (token === validateToken) error = formatMessage(err)
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

  // saveApiKey stores the key with no beginAction (see savingKey for why). On success the stored key changed, so the profile
  // preview (validated with the old key) is dropped, an earlier Validate error is cleared, and state is re-read. On
  // failure it rethrows: the form shows the error, and this banner stays untouched.
  async function saveApiKey(key) {
    if (savingKey) throw new AppMessage('A save is already in progress')
    savingKey = true
    try {
      await SaveAPIKey(key)
      // A Validate still in flight ran with the old key, so its result is dropped with the preview.
      validateToken++
      preview = null
      error = ''
      refreshError = false
      requestRefresh()
    } finally {
      savingKey = false
    }
  }

  async function syncNow() {
    // A second click while the first call is in flight must not start another sync. A clear in flight must not be
    // overtaken either: its SyncNow would take the request token, so the clear's result would be dropped.
    if (syncStarting || clearingData) return
    syncStarting = true
    const token = beginAction()
    try {
      const nextState = await SyncNow()
      applyActionState(token, nextState)
    } catch (err) {
      // "Already running" (also returned while a clear runs) is not a failure: a quiet read shows the run.
      if (isSyncAlreadyRunningError(err)) {
        // Hold the sync flag until the read lands, or the button would re-enable for that read.
        if (appState && token === stateRequestToken) appState = {...appState, syncInProgress: true}
        requestRefresh()
      } else if (token === stateRequestToken) {
        error = formatMessage(err)
      }
    } finally {
      syncStarting = false
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
    // The document is the scroller: reset before the swap, so the new view is never painted at the old scroll position.
    window.scrollTo({top: 0, behavior: 'instant'})
    activeView = value
    activeTagFilter = ''
  }

  function requestClearActiveUserData() {
    if (clearingData || !appState?.profile) return
    clearConfirmInput = ''
    clearConfirmOpen = true
  }

  function closeClearConfirm() {
    if (clearingData) return
    clearConfirmOpen = false
    clearConfirmInput = ''
  }

  // options comes from the modal: {deleteProfileData, deleteAPIKey}. Deleting the key leaves the returned state without
  // a key, so the missing-key screen takes over and the modal closes with the dashboard.
  async function confirmClearActiveUserData(options) {
    if (clearingData || !appState?.profile) return
    if (clearConfirmInput !== clearConfirmText) return
    clearingData = true
    const token = beginAction()
    try {
      const nextState = await ClearActiveUserData(Boolean(options?.deleteProfileData), Boolean(options?.deleteAPIKey))
      if (applyActionState(token, nextState)) {
        activeView = 'suggestions'
        activeTagFilter = ''
        profileInput = ''
        validateToken++
        preview = null
        clearConfirmOpen = false
        clearConfirmInput = ''
      }
    } catch (err) {
      if (token === stateRequestToken) error = formatMessage(err)
      // Close the dialog so the error banner is not hidden behind its backdrop.
      clearConfirmOpen = false
      clearConfirmInput = ''
      // The backend deletes the key before the later steps, so a failed call can still have removed the key. Re-read
      // the state so the screen matches it; the banner is kept (refreshError is false, so a successful read keeps it).
      requestRefresh()
    } finally {
      clearingData = false
      endAction()
    }
  }

  // A running action locks the menu: nothing may open it or move it.
  async function openGameMenu(event, game) {
    if (gameActionLoading) return
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
    // Nothing typed while the clear dialog is open may reach the page behind it.
    if (clearConfirmOpen) return false
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

  // titleBarHeight reads --title-bar-height (style.css), so the menu clears the bar however the bar is sized.
  function titleBarHeight() {
    const value = parseFloat(getComputedStyle(document.documentElement).getPropertyValue('--title-bar-height'))
    return Number.isFinite(value) && value > 0 ? value : 42
  }

  function positionGameMenu() {
    if (!contextMenu || !contextMenuEl) return
    const rect = contextMenuEl.getBoundingClientRect()
    const left = Math.max(menuMargin, Math.min(contextMenu.x, window.innerWidth - rect.width - menuMargin))
    // The top clamp starts below the title bar: the bar (z 1100) sits over the menu (z 1000).
    const top = Math.max(titleBarHeight() + menuMargin, Math.min(contextMenu.y, window.innerHeight - rect.height - menuMargin))
    contextMenu = {...contextMenu, left, top, ready: true}
  }

  async function runGameAction(action, selectedGame = contextMenu?.game) {
    if (!selectedGame || gameActionLoading) return
    const game = selectedGame
    // A button outside the menu (a sync-error row's Disable) closes another game's open menu instead of leaving it locked.
    if (contextMenu && contextMenu.game.entryKey !== game.entryKey) contextMenu = null
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

  const dashboard = $derived(appState?.dashboard || null)
  const latestSyncRun = $derived(dashboard?.latestSyncRun || null)
  const syncIsRunning = $derived(isSyncing(appState) || syncStarting)
  // While SyncNow is starting, the shell's state carries syncInProgress so the Sync button shows Syncing….
  const shellState = $derived(syncStarting && appState && !appState.syncInProgress ? {...appState, syncInProgress: true} : appState)
  const summary = $derived(dashboard?.summary || {})
  const suggestions = $derived(dashboard?.suggestions || [])
  const completed = $derived(sortByTitle(dashboard?.completed || []))
  const disabled = $derived(sortByTitle(dashboard?.disabled || []))
  const normalizedViewSearch = $derived(viewSearch.trim().toLowerCase())
  const warningGames = $derived((dashboard?.warnings || []).filter((game) => game.syncWarning))
  // Tag filter chips follow the active view's list; views without a tag filter get no games and no chips.
  const tagFilterGames = $derived(activeView === 'completed' ? completed : activeView === 'suggestions' ? suggestions : [])
  const tagFilterCounts = $derived(tagCountsFromGames(tagFilterGames))
  const visibleTagFilterOptions = $derived(suggestionTagOptions.filter((tag) => tagFilterCounts[tag] > 0))
  // Drops a tag filter the view no longer offers. $effect.pre: runs before the DOM update, so child effects (the grid reset) never see the stale list.
  $effect.pre(() => {
    // It terminates: the write sets activeTagFilter to '', so the rerun stops at the activeTagFilter check and writes nothing.
    if (activeTagFilter && !tagFilterCounts[activeTagFilter]) activeTagFilter = ''
  })
  const filteredSuggestions = $derived(suggestions.filter((game) => matchesViewFilters(game, normalizedViewSearch, activeTagFilter)))
  const filteredCompleted = $derived(completed.filter((game) => matchesViewFilters(game, normalizedViewSearch, activeTagFilter)))
  const filteredWarnings = $derived(warningGames.filter((game) => matchesSearchQuery(game, normalizedViewSearch)))
  const filteredDisabled = $derived(disabled.filter((game) => matchesSearchQuery(game, normalizedViewSearch)))
  const viewFiltered = $derived(Boolean(viewSearch.trim() || activeTagFilter))
  const clearConfirmText = $derived(appState?.profile ? appState.profile.steamId64 || appState.profile.displayName : '')
  // Same order as the screen switch below: the dashboard is the last branch.
  const dashboardShown = $derived(!loading && !appState?.initError && Boolean(appState?.apiKeyPresent) && Boolean(appState?.profileExists))
</script>

<svelte:window onclick={closeGameMenu} onkeydown={handleGlobalKeydown} />

<TitleBar
  dashboard={dashboardShown}
  {activeView}
  tabsDisabled={clearConfirmOpen}
  onActiveViewChange={changeView}
  onBarPress={closeGameMenu}
/>
<ScrollBar />

{#if loading}
  <main class="loading-screen">
    <div class="loader"></div>
    <p>Loading tracker…</p>
  </main>
{:else if appState?.initError}
  <SetupView mode="startup-error" state={appState} error={error} onRetry={retryKey} />
{:else if !appState?.apiKeyPresent}
  <SetupView mode="missing-key" state={appState} error={error} onRetry={retryKey} savingKey={savingKey} onSaveKey={saveApiKey} />
{:else if !appState?.profileExists}
  <SetupView
    mode="profile"
    state={appState}
    error={error}
    profileInput={profileInput}
    preview={preview}
    validating={validating}
    saving={saving}
    savingKey={savingKey}
    onValidate={validateProfile}
    onSave={saveProfile}
    onSaveKey={saveApiKey}
    onProfileInput={changeProfileInput}
  />
{:else}
  <DashboardShell
    state={shellState}
    {updates}
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
  <!-- Clicks inside the menu stop here, so the window click-away handler never closes it. -->
  <div bind:this={contextMenuEl} class="context-menu" role="menu" tabindex="-1" style={`left: ${contextMenu.left}px; top: ${contextMenu.top}px; visibility: ${contextMenu.ready ? 'visible' : 'hidden'};`} onclick={(event) => event.stopPropagation()} onkeydown={(event) => { event.stopPropagation(); if (event.key === 'Escape') closeGameMenu() }}>
    <p>{gameTitle(contextMenu.game)}</p>
    {#if activeView === 'suggestions'}
      <button type="button" onclick={() => runGameAction('previously-completed')} disabled={gameActionLoading}>{contextMenu.game.manualWasCompleted ? 'Clear previously completed flag' : 'Mark as previously completed'}</button>
      <button type="button" onclick={() => runGameAction('toggle-dlc')} disabled={gameActionLoading}>{contextMenu.game.missingAchievementsInDLC ? 'Clear DLC-missing flag' : 'Flag missing achievements in DLC'}</button>
    {/if}
    {#if contextMenu.game.disabled}
      <button type="button" onclick={() => runGameAction('enable')} disabled={gameActionLoading}>{contextMenu.game.isSet ? 'Re-enable set' : 'Re-enable game'}</button>
    {:else}
      <button type="button" class="danger" onclick={() => runGameAction('disable')} disabled={gameActionLoading}>{contextMenu.game.isSet ? 'Disable set' : 'Disable game'}</button>
    {/if}
  </div>
{/if}
