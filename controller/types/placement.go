package types

import (
	"strconv"
	"strings"
)

// FormationMaxProcessesTag is a host tag that caps how many tenant jobs
// (user apps and datastore instances) may run on that node. Omni and other
// system jobs are not counted. Missing, empty, 0, or "unlimited" means no cap.
const FormationMaxProcessesTag = "flynn-max-processes"

// ParseHostMaxProcesses reads FormationMaxProcessesTag from host tags.
// ok is false when the host has no cap.
func ParseHostMaxProcesses(tags map[string]string) (n int, ok bool) {
	if tags == nil {
		return 0, false
	}
	raw, present := tags[FormationMaxProcessesTag]
	if !present {
		return 0, false
	}
	s := strings.ToLower(strings.TrimSpace(raw))
	if s == "" || s == "unlimited" || s == "off" || s == "none" {
		return 0, false
	}
	n, err := strconv.Atoi(s)
	if err != nil || n < 1 {
		return 0, false
	}
	return n, true
}

// EncodeHostMaxProcesses stores n as FormationMaxProcessesTag.
// n < 1 clears the cap (empty value deletes the tag on the host).
func EncodeHostMaxProcesses(n int) string {
	if n < 1 {
		return ""
	}
	return strconv.Itoa(n)
}

// MergePlacementTags returns a copy of formation tags with runtime tags
// applied. Runtime tags win on key conflict so a named runtime cannot be
// escaped by a later scale.
func MergePlacementTags(formation, runtime map[string]string) map[string]string {
	if len(formation) == 0 && len(runtime) == 0 {
		return nil
	}
	out := make(map[string]string, len(formation)+len(runtime))
	for k, v := range formation {
		if k == "" {
			continue
		}
		out[k] = v
	}
	for k, v := range runtime {
		if k == "" {
			continue
		}
		out[k] = v
	}
	return out
}

// ProcessTagsEqual reports whether two process-type tag maps are the same.
func ProcessTagsEqual(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

// FormationTagsChanged reports whether any process type's tags differ.
func FormationTagsChanged(oldTags, newTags map[string]map[string]string) bool {
	if len(oldTags) != len(newTags) {
		// treat empty and nil as equal
		if len(oldTags) == 0 && len(newTags) == 0 {
			return false
		}
	}
	seen := make(map[string]struct{}, len(oldTags)+len(newTags))
	for k := range oldTags {
		seen[k] = struct{}{}
	}
	for k := range newTags {
		seen[k] = struct{}{}
	}
	for typ := range seen {
		if !ProcessTagsEqual(oldTags[typ], newTags[typ]) {
			return true
		}
	}
	return false
}

// ProcessesChanged reports whether any process count changed.
func ProcessesChanged(oldProc, newProc map[string]int) bool {
	if len(oldProc) == 0 && len(newProc) == 0 {
		return false
	}
	seen := make(map[string]struct{}, len(oldProc)+len(newProc))
	for k := range oldProc {
		seen[k] = struct{}{}
	}
	for k := range newProc {
		seen[k] = struct{}{}
	}
	for typ := range seen {
		if oldProc[typ] != newProc[typ] {
			return true
		}
	}
	return false
}
