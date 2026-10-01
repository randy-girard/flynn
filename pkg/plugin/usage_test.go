package plugin

import (
	"strings"
	"testing"
)

func TestColonName(t *testing.T) {
	if got := ColonName("pg", "psql"); got != "pg:psql" {
		t.Fatalf("pg psql: %q", got)
	}
	if got := ColonName("kafka", "topics create"); got != "kafka:topics:create" {
		t.Fatalf("kafka topics create: %q", got)
	}
	if got := ColonName("redis", "redis-cli"); got != "redis:cli" {
		t.Fatalf("redis-cli: %q", got)
	}
}

func TestExpandCLIUsageColonFirstSpaceFallback(t *testing.T) {
	c := &CLI{
		Command: "pg",
		Doc:     "usage: flynn pg\n       flynn pg:psql [--] [<argument>...]\n       flynn pg create <database>\n\nOpen a console.\n",
		Actions: []CLIAction{{Name: "psql"}, {Name: "create"}},
	}
	got := ExpandCLIUsage(c)
	colonIdx := strings.Index(got, "flynn pg:psql [--] [<argument>...]")
	spaceIdx := strings.Index(got, "flynn pg psql [--] [<argument>...]")
	if colonIdx < 0 || spaceIdx < 0 {
		t.Fatalf("missing colon or space form:\n%s", got)
	}
	if colonIdx > spaceIdx {
		t.Fatalf("colon form must be listed before space fallback:\n%s", got)
	}
	if !strings.Contains(got, "flynn pg:create <database>") {
		t.Fatalf("missing generated colon form:\n%s", got)
	}
	if strings.Count(got, "flynn pg:psql [--] [<argument>...]") != 1 {
		t.Fatalf("duplicated colon form:\n%s", got)
	}
}

func TestExpandCLIUsageRedisCliSpecialCase(t *testing.T) {
	c := &CLI{
		Command: "redis",
		Doc:     "usage: flynn redis redis-cli [--] [<argument>...]\n",
		Actions: []CLIAction{{Name: "redis-cli"}},
	}
	got := ExpandCLIUsage(c)
	if !strings.Contains(got, "flynn redis:cli [--] [<argument>...]") {
		t.Fatalf("canonical redis:cli missing:\n%s", got)
	}
	if !strings.Contains(got, "flynn redis redis-cli [--] [<argument>...]") {
		t.Fatalf("space fallback redis-cli missing:\n%s", got)
	}
}

func TestFoldColonBools(t *testing.T) {
	c := &CLI{Command: "pg", Actions: []CLIAction{{Name: "psql"}, {Name: "topics create"}}}
	bools := map[string]bool{"pg:psql": true}
	FoldColonBools(c, bools)
	if !bools["psql"] {
		t.Fatalf("pg:psql should fold onto psql: %#v", bools)
	}
	c = &CLI{Command: "kafka", Actions: []CLIAction{{Name: "topics create"}}}
	bools = map[string]bool{"kafka:topics:create": true}
	FoldColonBools(c, bools)
	if !bools["topics"] || !bools["create"] {
		t.Fatalf("nested colon should fold onto tokens: %#v", bools)
	}
	if got := c.MatchAction(bools); got == nil || got.Name != "topics create" {
		t.Fatalf("MatchAction after fold: %#v", got)
	}
}
