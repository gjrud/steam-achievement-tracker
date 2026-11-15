const defaultHandlers = {
  ClearActiveUserData: async () => missingHandler('ClearActiveUserData'),
  DisableGame: async () => missingHandler('DisableGame'),
  EnableGame: async () => missingHandler('EnableGame'),
  GetAppState: async () => missingHandler('GetAppState'),
  MarkGamePreviouslyCompleted: async () => missingHandler('MarkGamePreviouslyCompleted'),
  RetryAPIKey: async () => missingHandler('RetryAPIKey'),
  SaveProfile: async () => missingHandler('SaveProfile'),
  SetLibrarySettings: async () => missingHandler('SetLibrarySettings'),
  SyncNow: async () => missingHandler('SyncNow'),
  ToggleGameMissingAchievementsInDLC: async () => missingHandler('ToggleGameMissingAchievementsInDLC'),
  ValidateProfile: async () => missingHandler('ValidateProfile')
}

let handlers = {...defaultHandlers}

function missingHandler(name) {
  throw new Error(`Missing Wails mock handler: ${name}`)
}

export function __setWailsMocks(overrides = {}) {
  handlers = {...defaultHandlers, ...overrides}
}

export function __resetWailsMocks() {
  handlers = {...defaultHandlers}
}

export const ClearActiveUserData = (...args) => handlers.ClearActiveUserData(...args)
// Game actions take (appID, groupID): groupID selects one achievement set of the game.
export const DisableGame = (appID, groupID) => handlers.DisableGame(appID, groupID)
export const EnableGame = (appID, groupID) => handlers.EnableGame(appID, groupID)
export const GetAppState = (...args) => handlers.GetAppState(...args)
export const MarkGamePreviouslyCompleted = (appID, groupID) => handlers.MarkGamePreviouslyCompleted(appID, groupID)
export const RetryAPIKey = (...args) => handlers.RetryAPIKey(...args)
export const SaveProfile = (...args) => handlers.SaveProfile(...args)
export const SetLibrarySettings = (includeUnvettedApps, includePlayedFreeGames) => handlers.SetLibrarySettings(includeUnvettedApps, includePlayedFreeGames)
export const SyncNow = (...args) => handlers.SyncNow(...args)
export const ToggleGameMissingAchievementsInDLC = (appID, groupID) => handlers.ToggleGameMissingAchievementsInDLC(appID, groupID)
export const ValidateProfile = (...args) => handlers.ValidateProfile(...args)
