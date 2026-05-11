package agent

// BgTaskManagement groups methods for background task management.
type BgTaskManagement interface {
	GetBgTaskCount(sessionKey string) int
	ListBgTasks(sessionKey string) ([]BgTaskJSON, error)
	KillBgTask(taskID string) error
	CleanupCompletedBgTasks(sessionKey string)
}
