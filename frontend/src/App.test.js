import {cleanup, fireEvent, render, screen, waitFor, within} from '@testing-library/svelte'
import userEvent from '@testing-library/user-event'
import {afterEach, describe, expect, test, vi} from 'vitest'
import App from './App.svelte'
import {__resetWailsMocks, __setWailsMocks} from './test/wails-app-mock.js'

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
    expect(within(notCompletedPanel).getByRole('button', {name: /^Set\s+1$/})).toHaveClass('active')

    await user.click(screen.getByRole('button', {name: 'Completed'}))
    expect(await screen.findByRole('heading', {name: 'Completed DLC Pack'})).toBeInTheDocument()
    const completedPanel = screen.getByLabelText('Filter completed games by tag')
    expect(within(completedPanel).getByRole('button', {name: /^All\s+1$/})).toHaveClass('active')
    expect(within(completedPanel).getByRole('button', {name: /^Set\s+1$/})).not.toHaveClass('active')
    expect(screen.queryByRole('button', {name: 'Clear tag filter'})).not.toBeInTheDocument()

    await user.click(within(completedPanel).getByRole('button', {name: /^Set\s+1$/}))
    expect(screen.getByRole('heading', {name: 'Completed DLC Pack'})).toBeInTheDocument()
    expect(within(completedPanel).getByRole('button', {name: /^Set\s+1$/})).toHaveClass('active')

    await user.click(screen.getByRole('button', {name: 'Not completed'}))
    expect(await screen.findByRole('heading', {name: 'Vampire Survivors'})).toBeInTheDocument()
    notCompletedPanel = screen.getByLabelText('Filter not completed games by tag')
    expect(within(notCompletedPanel).getByRole('button', {name: /^All\s+2$/})).toHaveClass('active')
    expect(within(notCompletedPanel).getByRole('button', {name: /^Set\s+1$/})).not.toHaveClass('active')
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

describe('GameCard cover art', () => {
  test('marks header and main covers as landscape with an inline cover url', async () => {
    __setWailsMocks({
      GetAppState: vi.fn(async () => dashboardState({
        suggestions: [
          game({appid: 11, name: 'Header Cover', coverKind: 'header', coverUrl: '/game-covers/1/header.jpg?v=1'}),
          game({appid: 12, name: 'Main Cover', coverKind: 'main', coverUrl: '/game-covers/1/main.jpg?v=1'})
        ]
      }))
    })

    render(App)

    for (const name of ['Header Cover', 'Main Cover']) {
      const row = (await screen.findByRole('heading', {name})).closest('article')
      const cover = row.querySelector('.cover')

      expect(cover).toHaveClass('cover-landscape')
      expect(cover.getAttribute('style')).toContain('--cover-url')
      expect(cover.getAttribute('style')).toContain(`/game-covers/1/${name === 'Header Cover' ? 'header' : 'main'}.jpg?v=1`)
      expect(cover.querySelector('img').getAttribute('src')).toContain('/game-covers/1/')
    }
  })

  test('does not use landscape styling for library or hero covers', async () => {
    __setWailsMocks({
      GetAppState: vi.fn(async () => dashboardState({
        suggestions: [
          game({appid: 13, name: 'Library Cover', coverKind: 'library', coverUrl: '/game-covers/1/library.jpg?v=1'}),
          game({appid: 14, name: 'Hero Cover', coverKind: 'hero', coverUrl: '/game-covers/1/hero.jpg?v=1'})
        ]
      }))
    })

    render(App)

    for (const name of ['Library Cover', 'Hero Cover']) {
      const row = (await screen.findByRole('heading', {name})).closest('article')
      const cover = row.querySelector('.cover')

      expect(cover).toBeInTheDocument()
      expect(cover).not.toHaveClass('cover-landscape')
      expect(cover.getAttribute('style')).toContain('--cover-url')
    }
  })

  test('shows the placeholder without landscape styling when no cover url exists', async () => {
    __setWailsMocks({
      GetAppState: vi.fn(async () => dashboardState({
        suggestions: [game({appid: 15, name: 'No Cover', coverKind: 'header', coverUrl: ''})]
      }))
    })

    render(App)

    const row = (await screen.findByRole('heading', {name: 'No Cover'})).closest('article')
    const cover = row.querySelector('.cover')

    expect(cover).not.toHaveClass('cover-landscape')
    expect(cover).not.toHaveAttribute('style')
    expect(within(cover).getByText('Missing cover')).toBeInTheDocument()
    expect(cover.querySelector('img')).toBeNull()
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

function baseState() {
  return {
    appName: 'Steam Achievement Tracker',
    apiKeyPresent: true,
    apiKeyError: '',
    dataDir: '/tmp/steam-achievement-tracker',
    logFile: '/tmp/steam-achievement-tracker/logs/app.log',
    profileExists: false,
    profile: null,
    dashboard: null,
    syncInProgress: false,
    initError: '',
    secretSetupCommand: 'secret-tool store service steam-achievement-tracker username steam-web-api-key'
  }
}

function missingKeyState() {
  return {...baseState(), apiKeyPresent: false}
}

function profileSetupState() {
  return {...baseState(), apiKeyPresent: true, profileExists: false}
}

function dashboardState(overrides = {}) {
  const suggestions = overrides.suggestions ?? [
    game({appid: 1, name: 'Hades', tags: ['almost_there'], completionPercent: 86, unlockedAchievements: 43, totalAchievements: 50, missingAvgUnlock: 38, playtimeForever: 620}),
    game({appid: 2, name: 'Vampire Survivors', tags: ['new_achievements_added'], completionPercent: 96, unlockedAchievements: 120, totalAchievements: 125, playtimeForever: 900}),
    game({appid: 3, name: 'Empty Game', tags: ['no_achievements'], totalAchievements: 0, unlockedAchievements: 0, playtimeForever: 0})
  ]
  const warnings = overrides.warnings ?? [
    game({appid: 4, name: 'Broken Game', tags: ['sync_warning'], syncWarning: true, lastError: 'HTTP 503', lastErrorAt: '2026-05-22T10:00:00Z', lastSyncedAt: '2026-05-21T10:00:00Z'})
  ]
  const completed = overrides.completed ?? [
    game({appid: 6, name: 'Portal', tags: ['completed'], completionPercent: 100, unlockedAchievements: 15, totalAchievements: 15, playtimeForever: 120})
  ]
  const disabled = overrides.disabled ?? [
    game({appid: 5, name: 'Hidden Game', disabled: true, totalAchievements: 10, unlockedAchievements: 2, completionPercent: 20, playtimeForever: 70})
  ]

  return {
    ...baseState(),
    profileExists: true,
    profile: {steamId64: '76561198000000000', displayName: 'Test Player', avatarUrl: ''},
    dashboard: {
      summary: {
        ownedGamesCount: 6,
        completedGamesCount: completed.length,
        totalAchievementsUnlocked: 180
      },
      latestSyncRun: {
        status: 'partial',
        startedAt: '2026-05-22T10:00:00Z',
        finishedAt: '2026-05-22T10:05:00Z',
        gamesSynced: 5,
        gamesTotal: 6,
        gamesFailed: warnings.length
      },
      suggestions,
      completed,
      warnings,
      disabled
    }
  }
}

function game(overrides = {}) {
  const appid = overrides.appid ?? 0
  const groupId = overrides.groupId ?? 0
  return {
    appid,
    groupId,
    entryKey: `${appid}:${groupId}`,
    isSet: groupId !== 0,
    setName: '',
    dlcAppId: 0,
    coverKind: '',
    name: 'Game',
    coverUrl: '',
    totalAchievements: 10,
    unlockedAchievements: 0,
    completionPercent: 0,
    missingAvgUnlock: null,
    playtimeForever: 0,
    tags: [],
    syncWarning: false,
    lastError: '',
    lastErrorAt: '',
    lastSyncedAt: '',
    disabled: false,
    manualWasCompleted: false,
    missingAchievementsInDLC: false,
    dlcNotOwned: false,
    isCompleted: false,
    ...overrides
  }
}
