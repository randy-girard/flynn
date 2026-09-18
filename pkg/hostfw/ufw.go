package hostfw

import (
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

// Backend applies managed firewall rules. Tests use a fake; production uses UFW.
type Backend interface {
	List() ([]Rule, error)
	Allow(Rule) error
	Delete(Rule) error
}

// Runner executes a command and returns combined output.
type Runner func(name string, args ...string) (string, error)

func defaultRunner(name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

type UFWBackend struct {
	Run Runner
}

func (b UFWBackend) runner() Runner {
	if b.Run != nil {
		return b.Run
	}
	return defaultRunner
}

func (b UFWBackend) available() bool {
	_, err := exec.LookPath("ufw")
	return err == nil
}

func (b UFWBackend) List() ([]Rule, error) {
	if b.Run == nil && !b.available() {
		return nil, nil
	}
	out, err := b.runner()("ufw", "status")
	if err != nil {
		if b.Run == nil {
			return nil, nil
		}
		return nil, fmt.Errorf("ufw status: %w (%s)", err, strings.TrimSpace(out))
	}
	return ParseUFWStatus(out), nil
}

func (b UFWBackend) Allow(r Rule) error {
	if b.Run == nil && !b.available() {
		return nil
	}
	args := ufwAllowArgs(r)
	out, err := b.runner()("ufw", args...)
	if err != nil {
		return fmt.Errorf("ufw %s: %w (%s)", strings.Join(args, " "), err, strings.TrimSpace(out))
	}
	return nil
}

func (b UFWBackend) Delete(r Rule) error {
	if b.Run == nil && !b.available() {
		return nil
	}
	args := append([]string{"--force", "delete"}, ufwAllowArgs(r)...)
	out, err := b.runner()("ufw", args...)
	if err != nil {
		return fmt.Errorf("ufw delete: %w (%s)", err, strings.TrimSpace(out))
	}
	return nil
}

func ufwAllowArgs(r Rule) []string {
	args := []string{"allow"}
	if r.Port > 0 && r.From == "" {
		args = append(args, fmt.Sprintf("%d/tcp", r.Port))
	} else if r.Port > 0 && r.From != "" {
		args = append(args, "from", r.From, "to", "any", "port", strconv.Itoa(r.Port), "proto", "tcp")
	} else if r.From != "" {
		args = append(args, "from", r.From)
	} else {
		args = append(args, "in")
	}
	if r.Comment != "" {
		args = append(args, "comment", r.Comment)
	}
	return args
}

// ParseUFWStatus reads `ufw status` text into managed (and installer) rules.
func ParseUFWStatus(out string) []Rule {
	var rules []Rule
	seen := map[string]struct{}{}
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "Status:") || strings.HasPrefix(line, "To ") || strings.HasPrefix(line, "--") {
			continue
		}
		if strings.Contains(line, "(v6)") {
			continue
		}
		comment := ""
		if i := strings.Index(line, "#"); i >= 0 {
			comment = strings.TrimSpace(line[i+1:])
			line = strings.TrimSpace(line[:i])
		}
		kind := kindFromComment(comment)
		if kind == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 3 {
			continue
		}
		to, from := fields[0], fields[len(fields)-1]
		r := Rule{Kind: kind, Comment: comment, From: ""}
		if from != "Anywhere" {
			r.From = from
		}
		if port := tcpPortFromTo(to); port > 0 {
			r.Port = port
		}
		if _, ok := seen[r.Key()]; ok {
			continue
		}
		seen[r.Key()] = struct{}{}
		rules = append(rules, r)
	}
	return rules
}

func kindFromComment(comment string) string {
	switch comment {
	case CommentPublic:
		return KindPublic
	case CommentCluster:
		return KindCluster
	case CommentPeer:
		return KindPeer
	case CommentExpose:
		return KindExpose
	default:
		return ""
	}
}

func tcpPortFromTo(to string) int {
	to = strings.TrimSuffix(to, "/tcp")
	if to == "Anywhere" || strings.Contains(to, "/") {
		return 0
	}
	n := 0
	for _, c := range to {
		if c < '0' || c > '9' {
			return 0
		}
		n = n*10 + int(c-'0')
	}
	return n
}

// Reconcile adds missing peer/expose rules and deletes stale ones.
func Reconcile(b Backend, want Desired) error {
	if b == nil {
		return nil
	}
	have, err := b.List()
	if err != nil {
		return err
	}
	add, remove := Diff(have, Plan(want))
	for _, r := range remove {
		if err := b.Delete(r); err != nil {
			return err
		}
	}
	for _, r := range add {
		if err := b.Allow(r); err != nil {
			return err
		}
	}
	return nil
}
