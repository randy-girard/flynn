package types

import "strings"

// FormationHostIDsTag restricts a process type to the listed host IDs.
// It is a formation tag, not a host tag: the scheduler matches it against
// host.ID rather than host.Tags. Missing means all hosts; present but empty
// means no hosts.
const FormationHostIDsTag = "flynn-host-ids"

// EncodeHostIDsTag joins host IDs for FormationHostIDsTag.
func EncodeHostIDsTag(ids []string) string {
	cleaned := uniqueHostIDs(ids)
	return strings.Join(cleaned, ",")
}

// ParseHostIDsTag splits a FormationHostIDsTag value.
func ParseHostIDsTag(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return uniqueHostIDs(out)
}

// HostIDsTagMatches reports whether hostID is allowed by tags. A missing
// flynn-host-ids key matches every host; a present key matches only the
// listed IDs.
func HostIDsTagMatches(tags map[string]string, hostID string) bool {
	if tags == nil {
		return true
	}
	raw, ok := tags[FormationHostIDsTag]
	if !ok {
		return true
	}
	for _, id := range ParseHostIDsTag(raw) {
		if id == hostID {
			return true
		}
	}
	return false
}

func uniqueHostIDs(ids []string) []string {
	seen := make(map[string]struct{}, len(ids))
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out
}
