package mirror

import "sort"

// Plan describes the actions needed to reconcile local mirror with mem9.
type Plan struct {
	ToPost   []Entry // local entries without ID, novel content
	ToPut    []Entry // local entries with ID whose content differs from remote
	ToAppend []Entry // remote entries not present locally — append to local file
}

// Diff computes a sync plan. `local` is mirror entries; `remote` maps mem9_id → content.
func Diff(local []Entry, remote map[string]string) Plan {
	var plan Plan
	localIDs := map[string]bool{}

	for _, e := range local {
		if e.MemoryID == "" {
			plan.ToPost = append(plan.ToPost, e)
			continue
		}
		localIDs[e.MemoryID] = true
		if rc, ok := remote[e.MemoryID]; ok {
			if rc != e.Text {
				plan.ToPut = append(plan.ToPut, e)
			}
		}
		// stamped local + missing remote → no-op (preserve local)
	}

	// Stable order for ToAppend
	keys := make([]string, 0, len(remote))
	for id := range remote {
		if !localIDs[id] {
			keys = append(keys, id)
		}
	}
	sort.Strings(keys)
	for _, id := range keys {
		plan.ToAppend = append(plan.ToAppend, Entry{MemoryID: id, Text: remote[id]})
	}
	return plan
}
