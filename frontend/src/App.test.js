import {cleanup, fireEvent, render, screen, waitFor, within} from '@testing-library/svelte'
import userEvent from '@testing-library/user-event'
import {afterEach, describe, expect, test, vi} from 'vitest'
import App from './App.svelte'
import {__resetWailsMocks, __setWailsMocks} from './test/wails-app-mock.js'
import {
  baseState,
  dashboardState,
  failureMessage,
  game,
  hadesGame,
  missingKeyState,
  openClearDialog,
  openHadesMenu,
  profileSetupState,
  rejecting,
  syncingState
} from './test/app-fixtures.js'

afterEach(() => {
  cleanup()
  __resetWailsMocks()
  vi.restoreAllMocks()
})

describe('App setup states', () => {
  test('shows missing API key setup and retries key lookup', async () => {
    const retry = vi.fn(async () => profileSetupState())
    __setWailsMocks({
      GetAppState: vi.fn(async () => missingKeyState()),
      RetryAPIKey: retry
    })

    render(App)

    expect(await screen.findByRole('heading', {name: 'Steam Web API key missing'})).toBeInTheDocument()
    expect(screen.getByText(/secret-tool store/)).toBeInTheDocument()

    await fireEvent.click(screen.getByRole('button', {name: 'Retry key lookup'}))

    expect(retry).toHaveBeenCalledOnce()
    expect(await screen.findByRole('heading', {name: 'Connect your profile'})).toBeInTheDocument()
  })

  test('validates and saves a Steam profile', async () => {
    const preview = {steamId64: '76561198000000000', displayName: 'Test Player', avatarUrl: ''}
    const validate = vi.fn(async () => preview)
    const save = vi.fn(async () => dashboardState())
    __setWailsMocks({
      GetAppState: vi.fn(async () => profileSetupState()),
      ValidateProfile: validate,
      SaveProfile: save
    })
    const user = userEvent.setup()

    render(App)

    await user.type(await screen.findByPlaceholderText(/SteamID64/), 'test-player')
    await user.click(screen.getByRole('button', {name: 'Validate'}))

    expect(validate).toHaveBeenCalledWith('test-player')
    expect(await screen.findByText('Profile found')).toBeInTheDocument()
    expect(screen.getByRole('heading', {name: 'Test Player'})).toBeInTheDocument()

    await user.click(screen.getByRole('button', {name: 'Save and sync'}))

    expect(save).toHaveBeenCalledWith(preview)
    expect(await screen.findByRole('heading', {name: 'Dashboard'})).toBeInTheDocument()
  })

  test('shows startup init errors with log path', async () => {
    __setWailsMocks({GetAppState: vi.fn(async () => ({...baseState(), initError: 'database failed'}))})

    render(App)

    expect(await screen.findByRole('heading', {name: 'Startup failed'})).toBeInTheDocument()
    expect(screen.getByText('database failed')).toBeInTheDocument()
    expect(screen.getByText(/app.log/)).toBeInTheDocument()
  })
})

describe('App dashboard views', () => {
  test('switches views and filters suggestions by tag', async () => {
    __setWailsMocks({GetAppState: vi.fn(async () => dashboardState())})
    const user = userEvent.setup()

    render(App)

    expect(await screen.findByRole('heading', {name: 'Dashboard'})).toBeInTheDocument()
    expect(screen.getByRole('heading', {name: 'Hades'})).toBeInTheDocument()
    expect(screen.getByRole('heading', {name: 'Vampire Survivors'})).toBeInTheDocument()
    expect(screen.getByRole('button', {name: /Almost there\s+1/})).toBeInTheDocument()

    await user.click(screen.getByRole('button', {name: /Almost there\s+1/}))

    expect(screen.getByRole('heading', {name: 'Hades'})).toBeInTheDocument()
    expect(screen.queryByRole('heading', {name: 'Vampire Survivors'})).not.toBeInTheDocument()

    await user.click(screen.getByRole('button', {name: 'Clear tag filter'}))

    expect(screen.getByRole('heading', {name: 'Vampire Survivors'})).toBeInTheDocument()

    await user.click(screen.getByRole('button', {name: 'Completed'}))
    expect(await screen.findByRole('heading', {name: 'Portal'})).toBeInTheDocument()
    expect(screen.queryByRole('heading', {name: 'Hades'})).not.toBeInTheDocument()
    expect(screen.getByRole('button', {name: 'Completed'})).toHaveAttribute('aria-current', 'page')
    expect(screen.getByRole('button', {name: 'Not completed'})).not.toHaveAttribute('aria-current')
    expect(screen.getByRole('button', {name: 'Sync errors'})).not.toHaveAttribute('aria-current')

    await user.click(screen.getByRole('button', {name: 'Sync errors'}))
    expect(await screen.findByRole('heading', {name: 'Broken Game'})).toBeInTheDocument()
    expect(screen.getByText('HTTP 503')).toBeInTheDocument()
    expect(screen.getByRole('button', {name: 'Disable'})).toBeInTheDocument()

    await user.click(screen.getByRole('button', {name: 'Disabled'}))
    expect(await screen.findByRole('heading', {name: 'Hidden Game'})).toBeInTheDocument()
    expect(screen.getByRole('button', {name: 'Re-enable'})).toBeInTheDocument()

    await user.click(screen.getByRole('button', {name: 'Settings'}))
    expect(await screen.findByRole('heading', {name: 'Settings'})).toBeInTheDocument()
    expect(screen.getByText('/tmp/steam-achievement-tracker')).toBeInTheDocument()
    expect(screen.getByRole('button', {name: 'Clear data'})).toBeInTheDocument()
  })

  test('searches within the active view and shows empty states', async () => {
    __setWailsMocks({GetAppState: vi.fn(async () => dashboardState())})
    const user = userEvent.setup()

    render(App)

    const search = await screen.findByPlaceholderText('Search games in this view')
    await user.type(search, 'missing title')

    expect(screen.getByText('No games match the current filters.')).toBeInTheDocument()

    await user.clear(search)
    await user.click(screen.getByRole('button', {name: 'Completed'}))
    await user.type(screen.getByPlaceholderText('Search games in this view'), 'missing title')

    expect(screen.getByText('No matching games.')).toBeInTheDocument()
  })

  test('shows fresh sync failures as sync errors, not no achievements', async () => {
    __setWailsMocks({GetAppState: vi.fn(async () => dashboardState({warnings: [game({appid: 7, name: 'Fresh Failure', tags: ['sync_warning'], syncWarning: true, totalAchievements: 0, unlockedAchievements: 0, lastError: 'HTTP 503', lastErrorAt: '2026-05-22T10:00:00Z', lastSyncedAt: ''})]}))})
    const user = userEvent.setup()

    render(App)

    await user.click(await screen.findByRole('button', {name: 'Sync errors'}))
    const row = screen.getByRole('heading', {name: 'Fresh Failure'}).closest('article')

    expect(within(row).getAllByText('Sync error')).toHaveLength(2)
    expect(within(row).getByText('No previous sync data')).toBeInTheDocument()
    expect(within(row).queryByText('No achievements')).not.toBeInTheDocument()
  })

  test('shows playtime for no-achievement games when Steam reports it', async () => {
    __setWailsMocks({GetAppState: vi.fn(async () => dashboardState({suggestions: [game({appid: 8, name: 'Played No Achievements', tags: ['no_achievements'], totalAchievements: 0, unlockedAchievements: 0, playtimeForever: 75}), game({appid: 9, name: 'Unplayed No Achievements', tags: ['no_achievements', 'untouched'], totalAchievements: 0, unlockedAchievements: 0, playtimeForever: 0})]}))})

    render(App)

    const playedCard = (await screen.findByRole('heading', {name: 'Played No Achievements'})).closest('article')
    expect(within(playedCard).getAllByText('No achievements')).toHaveLength(1)
    expect(within(playedCard).getByText('1h 15m')).toBeInTheDocument()

    const unplayedCard = screen.getByRole('heading', {name: 'Unplayed No Achievements'}).closest('article')
    expect(within(unplayedCard).getAllByText('No achievements')).toHaveLength(1)
    expect(within(unplayedCard).queryByText('0m')).not.toBeInTheDocument()
  })

  test('clears active data through in-app confirmation modal', async () => {
    const clear = vi.fn(async () => profileSetupState())
    __setWailsMocks({
      GetAppState: vi.fn(async () => dashboardState()),
      ClearActiveUserData: clear
    })
    const user = userEvent.setup()

    render(App)

    await user.click(await screen.findByRole('button', {name: 'Settings'}))
    await user.click(screen.getByRole('button', {name: 'Clear data'}))

    const dialog = screen.getByRole('dialog', {name: 'Clear active user data?'})
    expect(within(dialog).getByText('76561198000000000')).toBeInTheDocument()
    expect(within(dialog).getByRole('button', {name: 'Copy confirmation value'})).toBeInTheDocument()
    expect(within(dialog).getByRole('button', {name: 'Clear data'})).toBeDisabled()

    await user.type(within(dialog).getByLabelText(/Type this exact value/), '76561198000000000')
    await user.click(within(dialog).getByRole('button', {name: 'Clear data'}))

    expect(clear).toHaveBeenCalledOnce()
    await waitFor(() => expect(screen.getByRole('heading', {name: 'Connect your profile'})).toBeInTheDocument())
  })

  test('calls game action handlers and refreshes dashboard state', async () => {
    const disable = vi.fn(async () => dashboardState({warnings: []}))
    const enable = vi.fn(async () => dashboardState({disabled: []}))
    __setWailsMocks({
      GetAppState: vi.fn(async () => dashboardState()),
      DisableGame: disable,
      EnableGame: enable
    })
    const user = userEvent.setup()

    render(App)

    await user.click(await screen.findByRole('button', {name: 'Sync errors'}))
    await user.click(screen.getByRole('button', {name: 'Disable'}))

    expect(disable).toHaveBeenCalledWith(4, 0)
    await waitFor(() => expect(screen.getByText('No sync errors.')).toBeInTheDocument())

    await user.click(screen.getByRole('button', {name: 'Disabled'}))
    await user.click(screen.getByRole('button', {name: 'Re-enable'}))

    expect(enable).toHaveBeenCalledWith(5, 0)
    await waitFor(() => expect(screen.getByText('No disabled games.')).toBeInTheDocument())
  })

  test('keys entries by entryKey so sibling sets of one app all render', async () => {
    __setWailsMocks({
      GetAppState: vi.fn(async () => dashboardState({
        suggestions: [
          game({appid: 10, groupId: 0, name: 'Team Fortress 2', totalAchievements: 40, unlockedAchievements: 20, completionPercent: 50}),
          game({appid: 10, groupId: 3, name: 'Team Fortress 2 – Scout', setName: 'Scout', totalAchievements: 9, unlockedAchievements: 1, completionPercent: 11}),
          game({appid: 10, groupId: 5, name: 'Team Fortress 2 – Pyro', setName: 'Pyro', totalAchievements: 9, unlockedAchievements: 0})
        ]
      }))
    })

    render(App)

    expect(await screen.findByRole('heading', {name: 'Team Fortress 2'})).toBeInTheDocument()
    expect(screen.getByRole('heading', {name: 'Team Fortress 2 – Scout'})).toBeInTheDocument()
    expect(screen.getByRole('heading', {name: 'Team Fortress 2 – Pyro'})).toBeInTheDocument()
    expect(screen.queryByText('Scout')).not.toBeInTheDocument()
    expect(screen.queryByText('Pyro')).not.toBeInTheDocument()
  })

  test('labels DLC set cards and explains the DLC-missing tag', async () => {
    __setWailsMocks({
      GetAppState: vi.fn(async () => dashboardState({
        suggestions: [
          game({appid: 4000, groupId: 1, name: 'PowerWash Simulator 2 – Pack', setName: 'PowerWash Simulator 2 – Pack', dlcAppId: 4173830, totalAchievements: 10, unlockedAchievements: 3, completionPercent: 30, tags: ['missing_achievements_in_dlc'], missingAchievementsInDLC: true})
        ]
      }))
    })

    render(App)

    const card = (await screen.findByRole('heading', {name: 'PowerWash Simulator 2 – Pack'})).closest('article')
    expect(within(card).getByText('DLC')).toBeInTheDocument()
    expect(within(card).getByText('Achievements in DLC').getAttribute('title')).toContain('card menu')
  })

  test('renders card tags in the same order as the filter chips', async () => {
    __setWailsMocks({
      GetAppState: vi.fn(async () => dashboardState({
        suggestions: [
          game({appid: 40, groupId: 2, dlcAppId: 4173840, name: 'Mixed Set', setName: 'Mixed Set', totalAchievements: 10, unlockedAchievements: 3, completionPercent: 30, playtimeForever: 60, tags: ['missing_cover_art', 'sync_warning', 'in_progress']})
        ],
        completed: [
          game({appid: 41, groupId: 1, dlcAppId: 4173841, name: 'Done Set', setName: 'Done Set', tags: ['missing_cover_art', 'completed', 'new_achievements_added'], isCompleted: true, completionPercent: 100, unlockedAchievements: 10, totalAchievements: 10})
        ]
      }))
    })
    const user = userEvent.setup()

    render(App)

    const mixedCard = (await screen.findByRole('heading', {name: 'Mixed Set'})).closest('article')
    expect(cardTagLabels(mixedCard)).toEqual(['In progress', 'Set', 'DLC', 'Missing cover art', 'Sync error'])

    await user.click(screen.getByRole('button', {name: 'Completed'}))
    const completedCard = (await screen.findByRole('heading', {name: 'Done Set'})).closest('article')
    expect(cardTagLabels(completedCard)).toEqual(['Completed', 'Set', 'DLC', 'Missing cover art'])
  })

  test('shows an unowned DLC set with a Not owned tag explained on hover', async () => {
    __setWailsMocks({
      GetAppState: vi.fn(async () => dashboardState({
        suggestions: [
          game({appid: 4000, groupId: 1, name: 'PowerWash Simulator 2 – Pack', setName: 'PowerWash Simulator 2 – Pack', dlcAppId: 4173830, totalAchievements: 10, unlockedAchievements: 0, completionPercent: 0, tags: ['dlc_not_owned'], dlcNotOwned: true})
        ]
      }))
    })

    render(App)

    const card = (await screen.findByRole('heading', {name: 'PowerWash Simulator 2 – Pack'})).closest('article')
    expect(within(card).getByText('Not owned').getAttribute('title')).toBe('You do not own the DLC for this achievement set.')
  })

  test('filter panel offers DLC and Not owned with counts and filters on click', async () => {
    __setWailsMocks({
      GetAppState: vi.fn(async () => dashboardState({
        suggestions: [
          game({appid: 4000, groupId: 1, name: 'DLC Owned Pack', setName: 'DLC Owned Pack', dlcAppId: 4173830, totalAchievements: 10, unlockedAchievements: 3, completionPercent: 30, playtimeForever: 60, tags: ['missing_achievements_in_dlc'], missingAchievementsInDLC: true}),
          game({appid: 4001, groupId: 1, name: 'Unowned Pack', setName: 'Unowned Pack', dlcAppId: 4173831, totalAchievements: 10, unlockedAchievements: 0, completionPercent: 0, tags: ['dlc_not_owned'], dlcNotOwned: true}),
          game({appid: 4002, name: 'Base Game', totalAchievements: 20, unlockedAchievements: 16, completionPercent: 80, playtimeForever: 300, tags: ['almost_there']})
        ]
      }))
    })
    const user = userEvent.setup()

    render(App)

    const panel = await screen.findByLabelText('Filter not completed games by tag')
    const dlcOption = within(panel).getByRole('button', {name: /^DLC\s+2$/})
    const notOwnedOption = within(panel).getByRole('button', {name: /^Not owned\s+1$/})
    expect(within(panel).getByRole('button', {name: /^Achievements in DLC\s+1$/})).toBeInTheDocument()
    expect(within(panel).getByRole('button', {name: /^All\s+3$/})).toBeInTheDocument()
    expect(within(panel).getByRole('button', {name: /^Set\s+2$/})).toBeInTheDocument()

    // Every DLC-bound set card shows the DLC chip, and the chip counts toward the filter.
    const ownedCard = screen.getByRole('heading', {name: 'DLC Owned Pack'}).closest('article')
    expect(within(ownedCard).getByText('DLC')).toBeInTheDocument()
    expect(within(screen.getByRole('heading', {name: 'Base Game'}).closest('article')).queryByText('DLC')).not.toBeInTheDocument()

    await user.click(dlcOption)
    expect(screen.getByRole('heading', {name: 'DLC Owned Pack'})).toBeInTheDocument()
    expect(screen.getByRole('heading', {name: 'Unowned Pack'})).toBeInTheDocument()
    expect(screen.queryByRole('heading', {name: 'Base Game'})).not.toBeInTheDocument()

    await user.click(notOwnedOption)
    expect(screen.queryByRole('heading', {name: 'DLC Owned Pack'})).not.toBeInTheDocument()
    expect(screen.getByRole('heading', {name: 'Unowned Pack'})).toBeInTheDocument()
    expect(screen.queryByRole('heading', {name: 'Base Game'})).not.toBeInTheDocument()

    await user.click(within(panel).getByRole('button', {name: 'Clear tag filter'}))
    expect(screen.getByRole('heading', {name: 'Base Game'})).toBeInTheDocument()
  })

  test('orders not completed filter chips like the Not completed groups', async () => {
    __setWailsMocks({
      GetAppState: vi.fn(async () => dashboardState({
        suggestions: [
          game({appid: 20, name: 'Late Sync', tags: ['sync_warning'], syncWarning: true}),
          game({appid: 21, name: 'Art Missing', tags: ['missing_cover_art']}),
          game({appid: 22, name: 'Unowned Set', groupId: 1, dlcAppId: 4173840, tags: ['dlc_not_owned'], dlcNotOwned: true}),
          game({appid: 23, name: 'No Achievements Game', tags: ['no_achievements']}),
          game({appid: 24, name: 'Untouched Game', tags: ['untouched']}),
          game({appid: 25, name: 'Flagged DLC Set', groupId: 2, dlcAppId: 4173841, tags: ['missing_achievements_in_dlc'], missingAchievementsInDLC: true}),
          game({appid: 26, name: 'Progress Game', tags: ['in_progress']}),
          game({appid: 27, name: 'Almost Game', tags: ['almost_there']}),
          game({appid: 28, name: 'New Achievements Game', tags: ['new_achievements_added']})
        ]
      }))
    })

    render(App)

    const panel = await screen.findByLabelText('Filter not completed games by tag')
    expect(chipLabels(panel)).toEqual(['All', 'New achievements added', 'Almost there', 'In progress', 'Untouched', 'Not owned', 'Achievements in DLC', 'No achievements', 'Set', 'DLC', 'Missing cover art', 'Sync error'])
  })

  test('gives the derived Set and DLC card tags their hover text', async () => {
    __setWailsMocks({
      GetAppState: vi.fn(async () => dashboardState({
        suggestions: [game({appid: 4000, groupId: 1, dlcAppId: 4173830, name: 'DLC Hover Set', setName: 'Pack', totalAchievements: 10, unlockedAchievements: 3, completionPercent: 30, tags: ['almost_there']})]
      }))
    })

    render(App)

    const row = (await screen.findByRole('heading', {name: 'DLC Hover Set'})).closest('article')
    expect(within(row).getByText('Set')).toHaveAttribute('title', 'Additional achievement set of this game')
    expect(within(row).getByText('DLC')).toHaveAttribute('title', 'Achievement set from DLC')
    expect(within(row).getByText('Almost there')).not.toHaveAttribute('title')
  })

  test('filters completed games by the tags their cards carry', async () => {
    __setWailsMocks({
      GetAppState: vi.fn(async () => dashboardState({
        completed: [
          game({appid: 6, name: 'Portal', tags: ['completed'], isCompleted: true, completionPercent: 100, unlockedAchievements: 15, totalAchievements: 15}),
          game({appid: 30, groupId: 1, name: 'Completed DLC Pack', setName: 'Completed DLC Pack', dlcAppId: 4173830, tags: ['completed'], isCompleted: true, completionPercent: 100, unlockedAchievements: 10, totalAchievements: 10}),
          game({appid: 31, name: 'Completed Missing Art', tags: ['completed', 'missing_cover_art'], isCompleted: true, completionPercent: 100, unlockedAchievements: 5, totalAchievements: 5}),
          game({appid: 32, name: 'Completed Stale Issue', tags: ['completed', 'new_achievements_added', 'missing_achievements_in_dlc'], isCompleted: true, completionPercent: 100, unlockedAchievements: 5, totalAchievements: 5})
        ]
      }))
    })
    const user = userEvent.setup()

    render(App)

    await user.click(await screen.findByRole('button', {name: 'Completed'}))

    const panel = await screen.findByLabelText('Filter completed games by tag')
    expect(chipLabels(panel)).toEqual(['All', 'Set', 'DLC', 'Missing cover art'])
    expect(within(panel).getByRole('button', {name: /^All\s+4$/})).toBeInTheDocument()
    expect(within(panel).getByRole('button', {name: /^Set\s+1$/})).toBeInTheDocument()
    expect(within(panel).getByRole('button', {name: /^DLC\s+1$/})).toBeInTheDocument()
    expect(within(panel).getByRole('button', {name: /^Missing cover art\s+1$/})).toBeInTheDocument()
    expect(within(screen.getByRole('heading', {name: 'Completed Stale Issue'}).closest('article')).queryByText('Achievements in DLC')).not.toBeInTheDocument()
    expect(within(screen.getByRole('heading', {name: 'Completed Stale Issue'}).closest('article')).queryByText('New achievements added')).not.toBeInTheDocument()

    await user.click(within(panel).getByRole('button', {name: /^DLC\s+1$/}))
    expect(screen.getByRole('heading', {name: 'Completed DLC Pack'})).toBeInTheDocument()
    expect(screen.queryByRole('heading', {name: 'Portal'})).not.toBeInTheDocument()
    expect(screen.queryByRole('heading', {name: 'Completed Missing Art'})).not.toBeInTheDocument()

    await user.type(screen.getByPlaceholderText('Search games in this view'), 'portal')
    expect(screen.getByText('No matching games.')).toBeInTheDocument()

    await user.click(within(panel).getByRole('button', {name: 'Clear tag filter'}))
    expect(screen.getByRole('heading', {name: 'Portal'})).toBeInTheDocument()
    expect(screen.queryByRole('heading', {name: 'Completed DLC Pack'})).not.toBeInTheDocument()
  })

  test('resets the tag filter when switching between Not completed and Completed', async () => {
    // Both views carry a set card (groupId 1, so the 'set' tag), so the Set chip stays available after a switch
    // and only the reset in changeView can clear the active filter.
    __setWailsMocks({
      GetAppState: vi.fn(async () => dashboardState({
        suggestions: [
          game({appid: 30, groupId: 1, name: 'Hades – Expansion', setName: 'Expansion', tags: ['almost_there'], completionPercent: 86, unlockedAchievements: 43, totalAchievements: 50}),
          game({appid: 2, name: 'Vampire Survivors', tags: ['new_achievements_added'], completionPercent: 96, unlockedAchievements: 120, totalAchievements: 125})
        ],
        completed: [
          game({appid: 31, groupId: 1, name: 'Completed DLC Pack', setName: 'Completed DLC Pack', dlcAppId: 4173830, tags: ['completed'], isCompleted: true, completionPercent: 100, unlockedAchievements: 10, totalAchievements: 10})
        ]
      }))
    })
    const user = userEvent.setup()

    render(App)

    let notCompletedPanel = await screen.findByLabelText('Filter not completed games by tag')
    await user.click(within(notCompletedPanel).getByRole('button', {name: /^Set\s+1$/}))
    expect(screen.getByRole('heading', {name: 'Hades – Expansion'})).toBeInTheDocument()
    expect(screen.queryByRole('heading', {name: 'Vampire Survivors'})).not.toBeInTheDocument()
    expect(within(notCompletedPanel).getByRole('button', {name: /^Set\s+1$/})).toHaveAttribute('aria-pressed', 'true')

    await user.click(screen.getByRole('button', {name: 'Completed'}))
    expect(await screen.findByRole('heading', {name: 'Completed DLC Pack'})).toBeInTheDocument()
    const completedPanel = screen.getByLabelText('Filter completed games by tag')
    expect(within(completedPanel).getByRole('button', {name: /^All\s+1$/})).toHaveAttribute('aria-pressed', 'true')
    expect(within(completedPanel).getByRole('button', {name: /^Set\s+1$/})).toHaveAttribute('aria-pressed', 'false')
    expect(screen.queryByRole('button', {name: 'Clear tag filter'})).not.toBeInTheDocument()

    await user.click(within(completedPanel).getByRole('button', {name: /^Set\s+1$/}))
    expect(screen.getByRole('heading', {name: 'Completed DLC Pack'})).toBeInTheDocument()
    expect(within(completedPanel).getByRole('button', {name: /^Set\s+1$/})).toHaveAttribute('aria-pressed', 'true')

    await user.click(screen.getByRole('button', {name: 'Not completed'}))
    expect(await screen.findByRole('heading', {name: 'Vampire Survivors'})).toBeInTheDocument()
    notCompletedPanel = screen.getByLabelText('Filter not completed games by tag')
    expect(within(notCompletedPanel).getByRole('button', {name: /^All\s+2$/})).toHaveAttribute('aria-pressed', 'true')
    expect(within(notCompletedPanel).getByRole('button', {name: /^Set\s+1$/})).toHaveAttribute('aria-pressed', 'false')
    expect(screen.queryByRole('button', {name: 'Clear tag filter'})).not.toBeInTheDocument()
  })

  test('passes groupId to game actions from a set card', async () => {
    const toggle = vi.fn(async () => dashboardState())
    __setWailsMocks({
      GetAppState: vi.fn(async () => dashboardState({
        suggestions: [game({appid: 10, groupId: 3, name: 'Team Fortress 2 – Scout', setName: 'Scout', totalAchievements: 9, unlockedAchievements: 1, completionPercent: 11})]
      })),
      ToggleGameMissingAchievementsInDLC: toggle
    })
    const user = userEvent.setup()

    render(App)

    const card = (await screen.findByRole('heading', {name: 'Team Fortress 2 – Scout'})).closest('article')
    await fireEvent.contextMenu(card)
    await user.click(await screen.findByRole('button', {name: 'Flag missing achievements in DLC'}))

    expect(toggle).toHaveBeenCalledWith(10, 3)
  })

  test('labels context menu disable action as set for set cards and game otherwise', async () => {
    const disable = vi.fn(async () => dashboardState())
    __setWailsMocks({
      GetAppState: vi.fn(async () => dashboardState({
        suggestions: [
          game({appid: 10, groupId: 0, name: 'Team Fortress 2', totalAchievements: 40, unlockedAchievements: 20, completionPercent: 50}),
          game({appid: 10, groupId: 3, name: 'Team Fortress 2 – Scout', setName: 'Scout', totalAchievements: 9, unlockedAchievements: 1, completionPercent: 11})
        ]
      })),
      DisableGame: disable
    })
    const user = userEvent.setup()

    render(App)

    const gameCard = (await screen.findByRole('heading', {name: 'Team Fortress 2'})).closest('article')
    await fireEvent.contextMenu(gameCard)
    expect(await screen.findByRole('button', {name: 'Disable game'})).toBeInTheDocument()
    expect(screen.queryByRole('button', {name: 'Disable set'})).not.toBeInTheDocument()

    const setCard = screen.getByRole('heading', {name: 'Team Fortress 2 – Scout'}).closest('article')
    await fireEvent.contextMenu(setCard)
    await user.click(await screen.findByRole('button', {name: 'Disable set'}))

    expect(disable).toHaveBeenCalledWith(10, 3)
  })

  test('disables only the selected set when a game has several sets', async () => {
    const disable = vi.fn(async () => dashboardState({warnings: []}))
    __setWailsMocks({
      GetAppState: vi.fn(async () => dashboardState({
        warnings: [game({appid: 4, groupId: 2, name: 'Broken Set', setName: 'Broken Set', syncWarning: true, lastError: 'HTTP 503', lastSyncedAt: '2026-05-21T10:00:00Z'})]
      })),
      DisableGame: disable
    })
    const user = userEvent.setup()

    render(App)

    await user.click(await screen.findByRole('button', {name: 'Sync errors'}))
    await user.click(screen.getByRole('button', {name: 'Disable'}))

    expect(disable).toHaveBeenCalledWith(4, 2)
  })
})

// Cover assertions use what the user sees: the cover image source, or the Missing cover placeholder. The landscape
// layout for header/main covers is drawn by app.css from the cover-landscape class on .cover. jsdom does not apply
// that CSS, so the class is the only DOM signal and the landscape table test asserts it directly.
describe('GameCard cover art', () => {
  test('renders each cover kind with its own image url', async () => {
    const covers = [
      ['Header Cover', 'header', '/game-covers/1/header.jpg?v=1'],
      ['Main Cover', 'main', '/game-covers/1/main.jpg?v=1'],
      ['Library Cover', 'library', '/game-covers/1/library.jpg?v=1'],
      ['Hero Cover', 'hero', '/game-covers/1/hero.jpg?v=1']
    ]
    __setWailsMocks({
      GetAppState: vi.fn(async () => dashboardState({
        suggestions: covers.map(([name, coverKind, coverUrl], index) => game({appid: 11 + index, name, coverKind, coverUrl}))
      }))
    })

    render(App)

    for (const [name, , coverUrl] of covers) {
      const row = (await screen.findByRole('heading', {name})).closest('article')

      expect(row.querySelector('img')).toHaveAttribute('src', coverUrl)
      expect(within(row).queryByText('Missing cover')).not.toBeInTheDocument()
    }
  })

  test('shows the Missing cover placeholder and no image when no cover url exists', async () => {
    __setWailsMocks({
      GetAppState: vi.fn(async () => dashboardState({
        suggestions: [game({appid: 15, name: 'No Cover', coverKind: 'header', coverUrl: ''})]
      }))
    })

    render(App)

    const row = (await screen.findByRole('heading', {name: 'No Cover'})).closest('article')

    expect(within(row).getByText('Missing cover')).toBeInTheDocument()
    expect(row.querySelector('img')).toBeNull()
  })

  test.each([
    ['header', true],
    ['main', true],
    ['library', false],
    ['hero', false]
  ])('marks %s covers cover-landscape: %s', async (coverKind, landscape) => {
    __setWailsMocks({
      GetAppState: vi.fn(async () => dashboardState({
        suggestions: [game({appid: 16, name: 'Landscape Check', coverKind, coverUrl: `/game-covers/16/${coverKind}.jpg?v=1`})]
      }))
    })

    render(App)

    const row = (await screen.findByRole('heading', {name: 'Landscape Check'})).closest('article')
    expect(row.querySelector('.cover').classList.contains('cover-landscape')).toBe(landscape)
  })
})

describe('App Wails call failures', () => {
  // mocks is a factory so every test gets fresh vi.fn() instances; a shared object would carry call counts across tests.
  const cases = [
    {
      name: 'GetAppState on startup',
      mocks: () => ({GetAppState: rejecting()}),
      act: async () => {}
    },
    {
      name: 'RetryAPIKey',
      mocks: () => ({GetAppState: vi.fn(async () => missingKeyState()), RetryAPIKey: rejecting()}),
      act: async (user) => user.click(await screen.findByRole('button', {name: 'Retry key lookup'}))
    },
    {
      name: 'ValidateProfile',
      mocks: () => ({GetAppState: vi.fn(async () => profileSetupState()), ValidateProfile: rejecting()}),
      act: async (user) => {
        await user.type(await screen.findByPlaceholderText(/SteamID64/), 'test-player')
        await user.click(screen.getByRole('button', {name: 'Validate'}))
      }
    },
    {
      name: 'SaveProfile',
      mocks: () => ({
        GetAppState: vi.fn(async () => profileSetupState()),
        ValidateProfile: vi.fn(async () => ({steamId64: '76561198000000000', displayName: 'Test Player', avatarUrl: ''})),
        SaveProfile: rejecting()
      }),
      act: async (user) => {
        await user.type(await screen.findByPlaceholderText(/SteamID64/), 'test-player')
        await user.click(screen.getByRole('button', {name: 'Validate'}))
        await user.click(await screen.findByRole('button', {name: 'Save and sync'}))
      }
    },
    {
      name: 'SyncNow',
      mocks: () => ({GetAppState: vi.fn(async () => dashboardState()), SyncNow: rejecting()}),
      act: async (user) => user.click(await screen.findByRole('button', {name: 'Sync now'}))
    },
    {
      name: 'DisableGame',
      mocks: () => ({GetAppState: vi.fn(async () => dashboardState()), DisableGame: rejecting()}),
      act: async (user) => {
        await user.click(await screen.findByRole('button', {name: 'Sync errors'}))
        await user.click(screen.getByRole('button', {name: 'Disable'}))
      }
    },
    {
      name: 'EnableGame',
      mocks: () => ({GetAppState: vi.fn(async () => dashboardState()), EnableGame: rejecting()}),
      act: async (user) => {
        await user.click(await screen.findByRole('button', {name: 'Disabled'}))
        await user.click(screen.getByRole('button', {name: 'Re-enable'}))
      }
    },
    {
      name: 'MarkGamePreviouslyCompleted',
      mocks: () => ({GetAppState: vi.fn(async () => dashboardState()), MarkGamePreviouslyCompleted: rejecting()}),
      act: async (user) => {
        await openHadesMenu()
        await user.click(screen.getByRole('button', {name: 'Mark as previously completed'}))
      }
    },
    {
      name: 'ToggleGameMissingAchievementsInDLC',
      mocks: () => ({GetAppState: vi.fn(async () => dashboardState()), ToggleGameMissingAchievementsInDLC: rejecting()}),
      act: async (user) => {
        await openHadesMenu()
        await user.click(screen.getByRole('button', {name: 'Flag missing achievements in DLC'}))
      }
    },
    {
      name: 'ClearActiveUserData',
      mocks: () => ({GetAppState: vi.fn(async () => dashboardState()), ClearActiveUserData: rejecting()}),
      act: async (user) => {
        const dialog = await openClearDialog(user)
        await user.type(within(dialog).getByLabelText(/Type this exact value/), '76561198000000000')
        await user.click(within(dialog).getByRole('button', {name: 'Clear data'}))
      }
    }
  ]

  for (const {name, mocks, act} of cases) {
    test(`shows the error when ${name} rejects`, async () => {
      __setWailsMocks(mocks())
      const user = userEvent.setup()

      render(App)
      await act(user)

      expect(await screen.findByText(failureMessage)).toBeInTheDocument()
    })
  }

  test('clears the error once the next action succeeds', async () => {
    __setWailsMocks({
      // Later polls report syncing so a stalled runner cannot flip the button back to idle.
      GetAppState: vi.fn()
        .mockResolvedValueOnce(dashboardState())
        .mockResolvedValue(syncingState(0)),
      SyncNow: vi.fn()
        .mockRejectedValueOnce(new Error(failureMessage))
        .mockResolvedValueOnce(syncingState(0))
    })
    const user = userEvent.setup()

    render(App)
    await user.click(await screen.findByRole('button', {name: 'Sync now'}))
    expect(await screen.findByText(failureMessage)).toBeInTheDocument()

    await user.click(screen.getByRole('button', {name: 'Sync now'}))

    expect(await screen.findByRole('button', {name: 'Syncing…'})).toBeDisabled()
    expect(screen.queryByText(failureMessage)).not.toBeInTheDocument()
  })
})

describe('App previously completed flag', () => {
  test('marks a game previously completed from the card menu and then offers to clear it', async () => {
    let manual = false
    const mark = vi.fn(async () => {
      manual = !manual
      return dashboardState({suggestions: [hadesGame({manualWasCompleted: manual})]})
    })
    __setWailsMocks({
      GetAppState: vi.fn(async () => dashboardState({suggestions: [hadesGame({manualWasCompleted: false})]})),
      MarkGamePreviouslyCompleted: mark
    })
    const user = userEvent.setup()

    render(App)
    await openHadesMenu()
    await user.click(await screen.findByRole('button', {name: 'Mark as previously completed'}))

    expect(mark).toHaveBeenCalledWith(1, 0)

    await openHadesMenu()
    await user.click(await screen.findByRole('button', {name: 'Clear previously completed flag'}))

    expect(mark).toHaveBeenCalledTimes(2)
    expect(mark).toHaveBeenLastCalledWith(1, 0)
  })

  test('keeps the card menu open and locked while the action runs', async () => {
    let resolveMark
    const mark = vi.fn(() => new Promise((resolve) => {
      resolveMark = resolve
    }))
    __setWailsMocks({GetAppState: vi.fn(async () => dashboardState()), MarkGamePreviouslyCompleted: mark})
    const user = userEvent.setup()

    render(App)
    await openHadesMenu()
    await user.click(await screen.findByRole('button', {name: 'Mark as previously completed'}))

    expect(screen.getByRole('button', {name: 'Mark as previously completed'})).toBeDisabled()
    expect(screen.getByRole('button', {name: 'Flag missing achievements in DLC'})).toBeDisabled()

    await user.keyboard('{Escape}')
    expect(screen.getByRole('menu')).toBeInTheDocument()

    resolveMark(dashboardState({suggestions: [hadesGame({manualWasCompleted: true})]}))
    await waitFor(() => expect(screen.queryByRole('menu')).not.toBeInTheDocument())
  })
})

describe('App keyboard shortcuts', () => {
  test('a typed letter outside a field starts the view search', async () => {
    __setWailsMocks({GetAppState: vi.fn(async () => dashboardState())})
    const user = userEvent.setup()

    render(App)
    const search = await screen.findByPlaceholderText('Search games in this view')
    await user.keyboard('vamp')

    expect(search).toHaveFocus()
    expect(search).toHaveValue('vamp')
    expect(screen.getByRole('heading', {name: 'Vampire Survivors'})).toBeInTheDocument()
    expect(screen.queryByRole('heading', {name: 'Hades'})).not.toBeInTheDocument()
  })

  test('does not start a search from modified keys or Escape', async () => {
    __setWailsMocks({GetAppState: vi.fn(async () => dashboardState())})
    const user = userEvent.setup()

    render(App)
    const search = await screen.findByPlaceholderText('Search games in this view')
    await user.keyboard('{Control>}k{/Control}')
    await user.keyboard('{Escape}')

    expect(search).not.toHaveFocus()
    expect(search).toHaveValue('')
  })

  test('typing while a button has focus does not start a search', async () => {
    __setWailsMocks({GetAppState: vi.fn(async () => dashboardState())})
    const user = userEvent.setup()

    render(App)
    await screen.findByPlaceholderText('Search games in this view')
    await user.click(screen.getByRole('button', {name: 'Completed'}))
    expect(screen.getByRole('button', {name: 'Completed'})).toHaveFocus()

    await user.keyboard('p')

    const search = screen.getByPlaceholderText('Search games in this view')
    expect(search).not.toHaveFocus()
    expect(search).toHaveValue('')
    expect(screen.getByRole('heading', {name: 'Portal'})).toBeInTheDocument()
  })

  test('typing on Settings does nothing, and search rebinds after returning to a list view', async () => {
    __setWailsMocks({GetAppState: vi.fn(async () => dashboardState())})
    const user = userEvent.setup()

    render(App)
    await user.click(await screen.findByRole('button', {name: 'Settings'}))
    expect(await screen.findByRole('heading', {name: 'Settings'})).toBeInTheDocument()
    // Move focus off the nav button so the key event targets document.body, the case the guard must allow.
    document.activeElement.blur()

    // Settings has no search input, so the keystroke must not start a search or throw.
    await user.keyboard('x')
    expect(screen.queryByPlaceholderText('Search games in this view')).not.toBeInTheDocument()
    expect(screen.getByRole('heading', {name: 'Settings'})).toBeInTheDocument()

    // Returning remounts the search input; it must be the element the handler focuses.
    await user.click(screen.getByRole('button', {name: 'Not completed'}))
    document.activeElement.blur()
    await user.keyboard('vamp')

    const search = screen.getByPlaceholderText('Search games in this view')
    expect(search).toHaveFocus()
    expect(search).toHaveValue('vamp')
    expect(screen.getByRole('heading', {name: 'Vampire Survivors'})).toBeInTheDocument()
    expect(screen.queryByRole('heading', {name: 'Hades'})).not.toBeInTheDocument()
  })

  test('Escape closes the card menu from inside the menu and from the page', async () => {
    __setWailsMocks({GetAppState: vi.fn(async () => dashboardState())})

    render(App)
    await openHadesMenu()
    const menu = await screen.findByRole('menu')
    await fireEvent.keyDown(menu, {key: 'Escape'})
    expect(screen.queryByRole('menu')).not.toBeInTheDocument()

    await openHadesMenu()
    await screen.findByRole('menu')
    await fireEvent.keyDown(document.body, {key: 'Escape'})
    expect(screen.queryByRole('menu')).not.toBeInTheDocument()
  })
})

describe('App clear-data confirmation', () => {
  test('Cancel closes the dialog without clearing and resets the typed value', async () => {
    const clear = vi.fn(async () => profileSetupState())
    __setWailsMocks({GetAppState: vi.fn(async () => dashboardState()), ClearActiveUserData: clear})
    const user = userEvent.setup()

    render(App)
    const dialog = await openClearDialog(user)
    await user.type(within(dialog).getByLabelText(/Type this exact value/), '7656')
    await user.click(within(dialog).getByRole('button', {name: 'Cancel'}))

    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
    expect(clear).not.toHaveBeenCalled()
    expect(screen.getByRole('heading', {name: 'Dashboard'})).toBeInTheDocument()

    const reopened = await openClearDialog(user)
    expect(within(reopened).getByLabelText(/Type this exact value/)).toHaveValue('')
  })

  test('Escape closes the dialog without clearing', async () => {
    const clear = vi.fn(async () => profileSetupState())
    __setWailsMocks({GetAppState: vi.fn(async () => dashboardState()), ClearActiveUserData: clear})
    const user = userEvent.setup()

    render(App)
    await openClearDialog(user)
    await user.keyboard('{Escape}')

    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
    expect(clear).not.toHaveBeenCalled()
  })

  test.each([
    ['the display name', 'Test Player'],
    ['a value with a trailing space', '76561198000000000 '],
    ['a truncated value', '7656119800000000']
  ])('keeps clearing disabled and ignores Enter for %s', async (_label, typed) => {
    const clear = vi.fn(async () => profileSetupState())
    __setWailsMocks({GetAppState: vi.fn(async () => dashboardState()), ClearActiveUserData: clear})
    const user = userEvent.setup()

    render(App)
    const dialog = await openClearDialog(user)
    await user.type(within(dialog).getByLabelText(/Type this exact value/), typed)

    expect(within(dialog).getByRole('button', {name: 'Clear data'})).toBeDisabled()
    await user.keyboard('{Enter}')

    expect(clear).not.toHaveBeenCalled()
    expect(screen.getByRole('dialog', {name: 'Clear active user data?'})).toBeInTheDocument()
  })

  test('Enter confirms once the exact value is typed', async () => {
    const clear = vi.fn(async () => profileSetupState())
    __setWailsMocks({GetAppState: vi.fn(async () => dashboardState()), ClearActiveUserData: clear})
    const user = userEvent.setup()

    render(App)
    const dialog = await openClearDialog(user)
    await user.type(within(dialog).getByLabelText(/Type this exact value/), '76561198000000000{Enter}')

    expect(clear).toHaveBeenCalledOnce()
    await waitFor(() => expect(screen.getByRole('heading', {name: 'Connect your profile'})).toBeInTheDocument())
  })
})

describe('App library sync settings', () => {
  function settingsState(settings) {
    return {...dashboardState(), settings}
  }

  test('saves toggled library settings and shows the returned state', async () => {
    const setSettings = vi.fn(async (includeUnvettedApps, includePlayedFreeGames) => settingsState({includeUnvettedApps, includePlayedFreeGames}))
    __setWailsMocks({
      GetAppState: vi.fn(async () => settingsState({includeUnvettedApps: true, includePlayedFreeGames: true})),
      SetLibrarySettings: setSettings
    })
    const user = userEvent.setup()

    render(App)
    await user.click(await screen.findByRole('button', {name: 'Settings'}))
    const unvetted = screen.getByRole('switch', {name: 'Include unvetted apps'})
    const playedFree = screen.getByRole('switch', {name: 'Include played free games'})
    expect(unvetted).toBeChecked()
    expect(playedFree).toBeChecked()

    await user.click(unvetted)

    expect(setSettings).toHaveBeenLastCalledWith(false, true)
    await waitFor(() => expect(unvetted).not.toBeChecked())
    expect(playedFree).toBeChecked()

    await user.click(playedFree)

    expect(setSettings).toHaveBeenLastCalledWith(false, false)
    await waitFor(() => expect(playedFree).not.toBeChecked())
    expect(setSettings).toHaveBeenCalledTimes(2)
    // Settings never start a sync, so the sync button stays idle.
    expect(screen.getByRole('button', {name: 'Sync now'})).toBeEnabled()
  })

  test('ignores toggle clicks while the save is in flight', async () => {
    let resolveSave
    const setSettings = vi.fn(() => new Promise((resolve) => {
      resolveSave = resolve
    }))
    __setWailsMocks({GetAppState: vi.fn(async () => dashboardState()), SetLibrarySettings: setSettings})
    const user = userEvent.setup()

    render(App)
    await user.click(await screen.findByRole('button', {name: 'Settings'}))
    await user.click(screen.getByRole('switch', {name: 'Include unvetted apps'}))

    const unvetted = screen.getByRole('switch', {name: 'Include unvetted apps'})
    const playedFree = screen.getByRole('switch', {name: 'Include played free games'})
    // Never `disabled`: that would dim the switch and drop focus mid-click.
    expect(unvetted).toBeEnabled()
    expect(unvetted).toHaveAttribute('aria-busy', 'true')
    expect(playedFree).toHaveAttribute('aria-busy', 'true')

    await user.click(playedFree)
    expect(playedFree).toBeChecked()
    expect(setSettings).toHaveBeenCalledTimes(1)

    resolveSave(settingsState({includeUnvettedApps: false, includePlayedFreeGames: true}))
    await waitFor(() => expect(unvetted).toHaveAttribute('aria-busy', 'false'))
    expect(unvetted).not.toBeChecked()
  })

  test('shows the error and keeps the stored setting when saving fails', async () => {
    __setWailsMocks({
      GetAppState: vi.fn(async () => dashboardState()),
      SetLibrarySettings: rejecting()
    })
    const user = userEvent.setup()

    render(App)
    await user.click(await screen.findByRole('button', {name: 'Settings'}))
    const unvetted = screen.getByRole('switch', {name: 'Include unvetted apps'})
    await user.click(unvetted)

    expect(await screen.findByText(failureMessage)).toBeInTheDocument()
    await waitFor(() => expect(unvetted).toBeChecked())
    expect(screen.getByRole('switch', {name: 'Include unvetted apps'})).toHaveAttribute('aria-busy', 'false')
  })
})

// cardTagLabels returns the tag chip labels rendered on one game card, in display order.
function cardTagLabels(card) {
  return Array.from(card.querySelectorAll('.tag-list .tag')).map((tag) => tag.textContent.trim())
}

// chipLabels returns the filter chip names in a tag filter panel, without counts.
function chipLabels(panel) {
  return within(panel).getAllByRole('button')
    .filter((button) => button.classList.contains('tag-filter'))
    .map((button) => button.textContent.replace(/\s*\d+$/, '').trim())
}
