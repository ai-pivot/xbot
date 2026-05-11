package protocol

import "fmt"

type EventCapability struct {
	EventType string `json:"event"`
	Versions  []int  `json:"versions"`
}

func Negotiate(local, remote []EventCapability, required []string) ([]EventCapability, error) {
	remoteMap := make(map[string][]int, len(remote))
	for _, c := range remote {
		remoteMap[c.EventType] = c.Versions
	}

	var result []EventCapability

	for _, lc := range local {
		rVersions, ok := remoteMap[lc.EventType]
		if !ok {
			continue
		}
		best := highestCommon(lc.Versions, rVersions)
		if best == 0 {
			continue
		}
		result = append(result, EventCapability{
			EventType: lc.EventType,
			Versions:  []int{best},
		})
	}

	for _, req := range required {
		found := false
		for _, c := range result {
			if c.EventType == req {
				found = true
				break
			}
		}
		if !found {
			return nil, fmt.Errorf("required event %q not supported by remote", req)
		}
	}

	return result, nil
}

func highestCommon(a, b []int) int {
	set := make(map[int]bool, len(b))
	for _, v := range b {
		set[v] = true
	}
	best := 0
	for _, v := range a {
		if set[v] && v > best {
			best = v
		}
	}
	return best
}
