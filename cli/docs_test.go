package main

import (
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// cliDocUndocumented lists registered `flynn` commands that are deliberately
// not in docs/content/cli.md. Each needs a reason; anything else registered
// must be documented.
var cliDocUndocumented = map[string]string{}

// userCLIDoc returns the part of docs/content/cli.md that covers `flynn`
// (everything before the `## flynn-host` section).
func userCLIDoc(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile("../docs/content/cli.md")
	if err != nil {
		t.Fatal(err)
	}
	doc := string(b)
	i := strings.Index(doc, "\n## flynn-host\n")
	if i < 0 {
		t.Fatal("docs/content/cli.md has no `## flynn-host` section")
	}
	return doc[:i]
}

// docMentionsCommand reports whether doc has a code span that starts with the
// command name (`name`, `name <arg>`, or `flynn name …`).
func docMentionsCommand(doc, name string) bool {
	re := regexp.MustCompile("`(?:flynn )?" + regexp.QuoteMeta(name) + "[` ]")
	return re.MatchString(doc)
}

func TestEveryCanonicalCommandIsInCLIDoc(t *testing.T) {
	doc := userCLIDoc(t)
	var missing []string
	for name := range commands {
		if _, ok := topAliases[name]; ok {
			continue // compatibility alias registration
		}
		if _, ok := cliDocUndocumented[name]; ok {
			continue
		}
		if !docMentionsCommand(doc, name) {
			missing = append(missing, name)
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Fatalf("registered flynn commands missing from the flynn part of docs/content/cli.md (document them, or add them to cliDocUndocumented with a reason):\n  %s", strings.Join(missing, "\n  "))
	}
}

func TestCLIDocAllowlistIsCurrent(t *testing.T) {
	doc := userCLIDoc(t)
	for name, reason := range cliDocUndocumented {
		if strings.TrimSpace(reason) == "" {
			t.Errorf("cliDocUndocumented[%q] has no reason", name)
		}
		if _, ok := commands[name]; !ok {
			t.Errorf("cliDocUndocumented[%q] is not a registered command; drop it", name)
		}
		if docMentionsCommand(doc, name) {
			t.Errorf("cliDocUndocumented[%q] is documented now; drop it from the allowlist", name)
		}
	}
}
