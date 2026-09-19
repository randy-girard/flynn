package types

import (
	"regexp"
	"strconv"
	"strings"

	"github.com/randy-girard/flynn/pkg/random"
)

const (
	// JobNameMetaKey is stored in job_cache.meta (not flynn-controller.*)
	// so it survives JobMetaFromMetadata stripping.
	JobNameMetaKey = "name"

	jobNameMax = 9999
)

var jobNamePattern = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_-]{0,31}\.[1-9][0-9]{0,3}$`)

// IsJobName reports whether s looks like a process-type.number label.
func IsJobName(s string) bool {
	return jobNamePattern.MatchString(strings.TrimSpace(s))
}

// AllocateJobName returns typ.N where N is 1–9999 and not in used.
func AllocateJobName(typ string, used []string) string {
	typ = sanitizeJobType(typ)
	taken := make(map[string]struct{}, len(used))
	for _, u := range used {
		taken[strings.TrimSpace(u)] = struct{}{}
	}
	for i := 0; i < 64; i++ {
		n := 1 + random.Math.Intn(jobNameMax)
		name := typ + "." + strconv.Itoa(n)
		if _, ok := taken[name]; !ok {
			return name
		}
	}
	return typ + "." + strconv.Itoa(1+random.Math.Intn(jobNameMax))
}

// JobNameFromMeta returns the stored short name, if any.
func JobNameFromMeta(meta map[string]string) string {
	if meta == nil {
		return ""
	}
	if n := strings.TrimSpace(meta[JobNameMetaKey]); n != "" {
		return n
	}
	return strings.TrimSpace(meta["flynn-controller.name"])
}

// EnsureJobName copies Name onto Meta["name"] and back so JSON and job_cache stay in sync.
func EnsureJobName(job *Job) {
	if job == nil {
		return
	}
	if job.Name == "" {
		job.Name = JobNameFromMeta(job.Meta)
	}
	if job.Name == "" {
		return
	}
	if job.Meta == nil {
		job.Meta = map[string]string{}
	}
	job.Meta[JobNameMetaKey] = job.Name
}

// JobDisplayName is the short label for lists and CLI (stored name, else type.digits from uuid).
func JobDisplayName(job *Job) string {
	if job == nil {
		return ""
	}
	if job.Name != "" {
		return job.Name
	}
	if n := JobNameFromMeta(job.Meta); n != "" {
		return n
	}
	typ := sanitizeJobType(job.Type)
	return typ + "." + digitsFromID(job.UUID, job.ID)
}

func sanitizeJobType(typ string) string {
	typ = strings.TrimSpace(typ)
	if typ == "" {
		return "run"
	}
	var b strings.Builder
	for i, r := range typ {
		switch {
		case r >= 'A' && r <= 'Z', r >= 'a' && r <= 'z':
			b.WriteRune(r)
		case (r >= '0' && r <= '9') || r == '_' || r == '-':
			if i == 0 {
				b.WriteByte('j')
			}
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
		if b.Len() >= 32 {
			break
		}
	}
	if b.Len() == 0 {
		return "run"
	}
	return b.String()
}

func digitsFromID(ids ...string) string {
	for _, id := range ids {
		hex := strings.Map(func(r rune) rune {
			if (r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') || (r >= 'A' && r <= 'F') {
				return r
			}
			return -1
		}, id)
		if len(hex) >= 4 {
			n, err := strconv.ParseUint(hex[:4], 16, 32)
			if err == nil {
				return strconv.FormatUint(n%jobNameMax+1, 10)
			}
		}
	}
	return "1"
}
