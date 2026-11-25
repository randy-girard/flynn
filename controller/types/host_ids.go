package types

import "strings"

// FormationHostIDsTag restricts a process type to the listed host IDs.
// It is a formation tag, not a host tag: the scheduler matches it against
// host.ID rather than host.Tags. Missing means all hosts; present but empty
// means no hosts.
const FormationHostIDsTag = "flynn-host-ids"

// FormationAvoidHostIDsTag excludes the listed host IDs. Auto-failover
// followers use it so a replica is never placed on the primary's host.
// Missing or empty means no hosts are excluded.
const FormationAvoidHostIDsTag = "flynn-avoid-host-ids"

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

// AvoidHostIDsTagMatches reports whether hostID is allowed by the denylist.
// A missing or empty flynn-avoid-host-ids key matches every host.
func AvoidHostIDsTagMatches(tags map[string]string, hostID string) bool {
	if tags == nil {
		return true
	}
	raw, ok := tags[FormationAvoidHostIDsTag]
	if !ok || strings.TrimSpace(raw) == "" {
		return true
	}
	for _, id := range ParseHostIDsTag(raw) {
		if id == hostID {
			return false
		}
	}
	return true
}

// PlacementTagsMatchHost reports whether a job with these placement tags
// (formation tags merged with runtime profile tags) may run on the host:
// flynn-host-ids and flynn-avoid-host-ids against its ID, every other tag
// except the process caps against its tags. The scheduler places with it,
// and the deployer uses it to know which hosts an omni process must cover.
func PlacementTagsMatchHost(tags map[string]string, hostID string, hostTags map[string]string) bool {
	if !HostIDsTagMatches(tags, hostID) || !AvoidHostIDsTagMatches(tags, hostID) {
		return false
	}
	for k, v := range tags {
		if k == FormationHostIDsTag || k == FormationAvoidHostIDsTag || k == FormationMaxProcessesTag || k == FormationMaxProcessesSourceTag || k == FormationMaxPersistentSingletonsTag {
			continue
		}
		if w, ok := hostTags[k]; !ok || v != w {
			return false
		}
	}
	return true
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
