const defaultHandlers = {
  ClearActiveUserData: async () => missingHandler('ClearActiveUserData'),
  DisableGame: async () => missingHandler('DisableGame'),
  EnableGame: async () => missingHandler('EnableGame'),
  GetAppState: async () => missingHandler('GetAppState'),
  MarkGamePreviouslyCompleted: async () => missingHandler('MarkGamePreviouslyCompleted'),
  RetryAPIKey: async () => missingHandler('RetryAPIKey'),
  SaveProfile: async () => missingHandler('SaveProfile'),
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
export const DisableGame = (...args) => handlers.DisableGame(...args)
export const EnableGame = (...args) => handlers.EnableGame(...args)
export const GetAppState = (...args) => handlers.GetAppState(...args)
export const MarkGamePreviouslyCompleted = (...args) => handlers.MarkGamePreviouslyCompleted(...args)
export const RetryAPIKey = (...args) => handlers.RetryAPIKey(...args)
export const SaveProfile = (...args) => handlers.SaveProfile(...args)
export const SyncNow = (...args) => handlers.SyncNow(...args)
export const ToggleGameMissingAchievementsInDLC = (...args) => handlers.ToggleGameMissingAchievementsInDLC(...args)
export const ValidateProfile = (...args) => handlers.ValidateProfile(...args)
