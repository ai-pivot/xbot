package protocol

// CommandInfo describes a visible command independently from its execution
// handler. It is shared by local and remote clients so help, completion, and
// command-palette surfaces consume the same metadata.
type CommandInfo struct {
	Name        string   `json:"name"`
	Aliases     []string `json:"aliases,omitempty"`
	Usage       string   `json:"usage,omitempty"`
	Description string   `json:"description,omitempty"`
	Hidden      bool     `json:"-"`
	// AdminOnly marks management commands (subscription/settings/plugin
	// mutation) that only the operator may execute. After the multi-user
	// removal, subscriptions and settings are GLOBAL — a non-admin channel
	// user (e.g. a Feishu group member not listed in agent.admins) must not
	// mutate the operator's configuration. Dispatch points reject non-admin
	// senders before Execute.
	AdminOnly bool `json:"admin_only,omitempty"`
}

// MergeCommandInfoLists merges command metadata in group order. The first
// occurrence owns both the position and every non-empty field, matching the
// first-match command dispatch rules. Later occurrences only fill missing
// presentation metadata.
func MergeCommandInfoLists(groups ...[]CommandInfo) []CommandInfo {
	positions := make(map[string]int)
	merged := make([]CommandInfo, 0)
	for _, group := range groups {
		for _, info := range group {
			if info.Name == "" || info.Hidden {
				continue
			}
			if pos, ok := positions[info.Name]; ok {
				current := merged[pos]
				if len(current.Aliases) == 0 {
					current.Aliases = info.Aliases
				}
				if current.Usage == "" {
					current.Usage = info.Usage
				}
				if current.Description == "" {
					current.Description = info.Description
				}
				merged[pos] = current
				continue
			}
			positions[info.Name] = len(merged)
			merged = append(merged, info)
		}
	}
	return merged
}
