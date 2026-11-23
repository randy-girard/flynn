package cli

import (
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// hostCLIDocUndocumented lists registered `flynn-host` commands that are
// deliberately not in the flynn-host part of docs/content/cli.md. Each needs a
// reason; anything else registered must be documented.
var hostCLIDocUndocumented = map[string]string{}

// hostCLIDoc returns the `## flynn-host` section of docs/content/cli.md.
func hostCLIDoc(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile("../../docs/content/cli.md")
	if err != nil {
		t.Fatal(err)
	}
	doc := string(b)
	i := strings.Index(doc, "\n## flynn-host\n")
	if i < 0 {
		t.Fatal("docs/content/cli.md has no `## flynn-host` section")
	}
	return doc[i:]
}

// hostDocMentionsCommand reports whether doc has a code span that starts with
// the command name (`name`, `name <arg>`, or `flynn-host name …`).
func hostDocMentionsCommand(doc, name string) bool {
	re := regexp.MustCompile("`(?:(?:sudo )?flynn-host )?" + regexp.QuoteMeta(name) + "[` ]")
	return re.MatchString(doc)
}

func TestEveryCanonicalCommandIsInCLIDoc(t *testing.T) {
	doc := hostCLIDoc(t)
	var missing []string
	for name := range commands {
		if _, ok := hyphenAliases[name]; ok {
			continue // compatibility alias registration
		}
		if _, ok := hostPluginHelpSkip[hostCommandParent(name)]; ok {
			continue // hidden duplicate parent (acme is letsencrypt); see TestHiddenAliasParentsAreNamedInCLIDoc
		}
		if _, ok := hostCLIDocUndocumented[name]; ok {
			continue
		}
		if !hostDocMentionsCommand(doc, name) {
			missing = append(missing, name)
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Fatalf("registered flynn-host commands missing from the flynn-host section of docs/content/cli.md (document them, or add them to hostCLIDocUndocumented with a reason):\n  %s", strings.Join(missing, "\n  "))
	}
}

func hostCommandParent(name string) string {
	if i := strings.IndexByte(name, ':'); i >= 0 {
		return name[:i]
	}
	return name
}

// The hidden duplicate parents (acme) are not listed one by one, but the doc
// must say they are an alias family.
func TestHiddenAliasParentsAreNamedInCLIDoc(t *testing.T) {
	doc := hostCLIDoc(t)
	for parent := range hostPluginHelpSkip {
		if !strings.Contains(doc, "`"+parent+":*`") {
			t.Errorf("flynn-host section of docs/content/cli.md does not name the %s alias family as `%s:*`", parent, parent)
		}
	}
}

func TestCLIDocAllowlistIsCurrent(t *testing.T) {
	doc := hostCLIDoc(t)
	for name, reason := range hostCLIDocUndocumented {
		if strings.TrimSpace(reason) == "" {
			t.Errorf("hostCLIDocUndocumented[%q] has no reason", name)
		}
		if _, ok := commands[name]; !ok {
			t.Errorf("hostCLIDocUndocumented[%q] is not a registered command; drop it", name)
		}
		if hostDocMentionsCommand(doc, name) {
			t.Errorf("hostCLIDocUndocumented[%q] is documented now; drop it from the allowlist", name)
		}
	}
}
