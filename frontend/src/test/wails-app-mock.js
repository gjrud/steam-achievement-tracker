const defaultHandlers = {
  CancelUpdate: async () => missingHandler('CancelUpdate'),
  CheckForUpdates: async () => missingHandler('CheckForUpdates'),
  ClearActiveUserData: async (deleteProfileData, deleteAPIKey) => missingHandler('ClearActiveUserData'),
  DisableGame: async () => missingHandler('DisableGame'),
  EnableGame: async () => missingHandler('EnableGame'),
  GetAppState: async () => missingHandler('GetAppState'),
  GetUpdateState: async () => missingHandler('GetUpdateState'),
  InstallUpdate: async (version) => missingHandler('InstallUpdate'),
  MarkGamePreviouslyCompleted: async () => missingHandler('MarkGamePreviouslyCompleted'),
  RestartToUpdate: async () => missingHandler('RestartToUpdate'),
  RetryAPIKey: async () => missingHandler('RetryAPIKey'),
  SaveAPIKey: async () => missingHandler('SaveAPIKey'),
  SaveProfile: async () => missingHandler('SaveProfile'),
  SetLibrarySettings: async () => missingHandler('SetLibrarySettings'),
  SetUpdateChecksEnabled: async (enabled) => missingHandler('SetUpdateChecksEnabled'),
  SyncNow: async () => missingHandler('SyncNow'),
  ToggleGameMissingAchievementsInDLC: async () => missingHandler('ToggleGameMissingAchievementsInDLC'),
  ValidateProfile: async () => missingHandler('ValidateProfile')
}

let handlers = {...defaultHandlers}

function missingHandler(name) {
  throw new Error(`Missing Wails mock handler: ${name}`)
}

// A handler that simulates a Go error rejects with a RuntimeError (wails-runtime-mock.js), as the real bound call does.
// Any other rejection stands for a frontend or argument error, which the app shows as the generic sentence.
export function __setWailsMocks(overrides = {}) {
  handlers = {...defaultHandlers, ...overrides}
}

export function __resetWailsMocks() {
  handlers = {...defaultHandlers}
}

// Parameter lists mirror the generated bindings exactly; the drift test compares argument counts.
export const CancelUpdate = () => handlers.CancelUpdate()
export const CheckForUpdates = () => handlers.CheckForUpdates()
// Booleans select what else to delete: profile data and the system keyring API key. Both false keeps everything.
export const ClearActiveUserData = (deleteProfileData, deleteAPIKey) => handlers.ClearActiveUserData(deleteProfileData, deleteAPIKey)
// Game actions take (appID, groupID): groupID selects one achievement set of the game.
export const DisableGame = (appID, groupID) => handlers.DisableGame(appID, groupID)
export const EnableGame = (appID, groupID) => handlers.EnableGame(appID, groupID)
export const GetAppState = () => handlers.GetAppState()
export const GetUpdateState = () => handlers.GetUpdateState()
// InstallUpdate takes the version the user saw; the backend refuses it if the newest release has changed.
export const InstallUpdate = (version) => handlers.InstallUpdate(version)
export const MarkGamePreviouslyCompleted = (appID, groupID) => handlers.MarkGamePreviouslyCompleted(appID, groupID)
export const RestartToUpdate = () => handlers.RestartToUpdate()
export const RetryAPIKey = () => handlers.RetryAPIKey()
export const SaveAPIKey = (key) => handlers.SaveAPIKey(key)
export const SaveProfile = (preview) => handlers.SaveProfile(preview)
export const SetLibrarySettings = (includeUnvettedApps, includePlayedFreeGames) => handlers.SetLibrarySettings(includeUnvettedApps, includePlayedFreeGames)
export const SetUpdateChecksEnabled = (enabled) => handlers.SetUpdateChecksEnabled(enabled)
export const SyncNow = () => handlers.SyncNow()
export const ToggleGameMissingAchievementsInDLC = (appID, groupID) => handlers.ToggleGameMissingAchievementsInDLC(appID, groupID)
export const ValidateProfile = (input) => handlers.ValidateProfile(input)
