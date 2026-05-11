package agent

// SettingsManagement groups methods for runtime settings management.
type SettingsManagement interface {
	GetSettings(namespace, senderID string) (map[string]string, error)
	SetSetting(namespace, senderID, key, value string) error
	SetTUICallbacks(
		tuiCtrl func(action string, params map[string]string) (map[string]string, error),
		configGet func(key string) (string, error),
		configSet func(key, value string) (string, error),
	)
	SetTUIControlHandler(callback func(action string, params map[string]string) (map[string]string, error))
}
