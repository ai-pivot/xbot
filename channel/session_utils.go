package channel

import (
	"crypto/rand"
	"fmt"
	"math/big"
	"strings"
)

// autoNamePrefix is the prefix for auto-generated session names.
const autoNamePrefix = "Agent-"

// sessionAdj and sessionNoun provide word lists for generating natural-sounding session names
// like "Agent-brave-fox" or "Agent-calm-stone". 16×16 = 256 unique combinations.
var (
	sessionAdj = []string{
		"brave", "calm", "swift", "keen", "warm", "witty", "sage", "brisk",
		"cool", "bold", "sharp", "lucid", "sunny", "frank", "deft", "astute",
	}
	sessionNoun = []string{
		"fox", "hawk", "lynx", "dove", "panda", "otter", "falcon", "heron",
		"stone", "flame", "brook", "cedar", "comet", "coral", "ember", "zephyr",
	}
)

// NameEntry is a lightweight name/chatID pair used for dedup lookups.
type NameEntry struct {
	Name   string
	ChatID string
}

// NameLookup returns session name entries for deduplication.
// Implemented differently for local JSON vs DB.
type NameLookup func() []NameEntry

// DeduplicateSessionName appends a random "-adj-noun" suffix when the desired name
// already exists in the existingNames set. The excludeChatID's own name is skipped
// so that renaming a session to its current name is a no-op.
// Returns the deduplicated name (unchanged if no collision).
func DeduplicateSessionName(desired string, excludeChatID string, lookup NameLookup) string {
	names := lookup()
	// Check collision: any OTHER session has the same name?
	for _, entry := range names {
		if entry.Name == desired && entry.ChatID != excludeChatID {
			goto collide
		}
	}
	return desired

collide:
	// Pick random adj-noun suffix until unique (max 10 attempts, then fall back to counter).
	for i := 0; i < 10; i++ {
		adjIdx, _ := rand.Int(rand.Reader, big.NewInt(int64(len(sessionAdj))))
		nounIdx, _ := rand.Int(rand.Reader, big.NewInt(int64(len(sessionNoun))))
		candidate := desired + "-" + sessionAdj[adjIdx.Int64()] + "-" + sessionNoun[nounIdx.Int64()]
		conflict := false
		for _, entry := range names {
			if entry.Name == candidate {
				conflict = true
				break
			}
		}
		if !conflict {
			return candidate
		}
	}
	// Extremely unlikely: fall back to numeric suffix
	for n := 2; n < 100; n++ {
		candidate := fmt.Sprintf("%s-%d", desired, n)
		conflict := false
		for _, entry := range names {
			if entry.Name == candidate {
				conflict = true
				break
			}
		}
		if !conflict {
			return candidate
		}
	}
	return desired // give up, shouldn't happen
}

// GenerateSessionName creates a random session name like "Agent-brave-fox".
// Exported so storage/sqlite can use it for web chat default labels.
func GenerateSessionName() (string, error) {
	adjIdx, err := rand.Int(rand.Reader, big.NewInt(int64(len(sessionAdj))))
	if err != nil {
		return "", err
	}
	nounIdx, err := rand.Int(rand.Reader, big.NewInt(int64(len(sessionNoun))))
	if err != nil {
		return "", err
	}
	return autoNamePrefix + sessionAdj[adjIdx.Int64()] + "-" + sessionNoun[nounIdx.Int64()], nil
}

// IsAutoSessionName returns true if the name looks like an auto-generated session name.
func IsAutoSessionName(name string) bool {
	return strings.HasPrefix(name, autoNamePrefix)
}
