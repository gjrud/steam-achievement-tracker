package config

const (
	AppName       = "Steam Achievement Tracker"
	AppID         = "steam-achievement-tracker"
	DataDirName   = ".steam-achievement-tracker"
	DBFileName    = "steam-achievement-tracker.db"
	KeyringUser   = "steam-web-api-key"
	SchemaVersion = 8
)

const SecretSetupCommand = `secret-tool store \
  --label="Steam Achievement Tracker - Steam Web API Key" \
  service steam-achievement-tracker \
  username steam-web-api-key`
