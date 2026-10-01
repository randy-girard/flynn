package plugin

import (
	"strings"

	"github.com/randy-girard/flynn/pkg/clihelp"
)

// ActionHelp is the user-facing help for one plugin command
// (`flynn help pg:create`, `flynn pg:create --help`). It is that command's
// usage, description, options, and examples — not the whole plugin cli.doc.
func (c *CLI) ActionHelp(full string) string {
	if c == nil || strings.TrimSpace(full) == "" {
		return ""
	}
	patterns := actionUsagePatterns(c, full)
	if len(patterns) == 0 {
		return ""
	}
	var b strings.Builder
	for i, p := range patterns {
		if i == 0 {
			b.WriteString("usage: ")
		} else {
			b.WriteString("       ")
		}
		b.WriteString(p)
		b.WriteByte('\n')
	}
	if desc := actionHelpDescription(c, full); desc != "" {
		b.WriteByte('\n')
		b.WriteString(desc)
		if !strings.HasSuffix(desc, ".") {
			b.WriteByte('.')
		}
		b.WriteByte('\n')
	}
	if prose := actionHelpProse(c.Doc, full, c); prose != "" {
		b.WriteByte('\n')
		b.WriteString(prose)
		if !strings.HasSuffix(prose, "\n") {
			b.WriteByte('\n')
		}
	}
	if opts := actionHelpOptions(c.Doc, patterns); opts != "" {
		b.WriteString("\nOptions:\n")
		b.WriteString(opts)
		if !strings.HasSuffix(opts, "\n") {
			b.WriteByte('\n')
		}
	}
	if ex := actionHelpExamples(c.Doc, full, c); ex != "" {
		b.WriteString("\nExamples:\n\n")
		b.WriteString(ex)
		if !strings.HasSuffix(ex, "\n") {
			b.WriteByte('\n')
		}
	}
	return b.String()
}

func (c *CLI) actionByColon(full string) *CLIAction {
	if c == nil {
		return nil
	}
	for i := range c.Actions {
		if ColonName(c.Command, c.Actions[i].Name) == full {
			return &c.Actions[i]
		}
	}
	return nil
}

func actionUsagePrefixes(c *CLI, full string) []string {
	out := []string{"flynn " + full}
	if a := c.actionByColon(full); a != nil {
		out = append(out, "flynn "+c.Command+" "+a.Name)
	} else {
		out = append(out, "flynn "+strings.ReplaceAll(full, ":", " "))
	}
	return uniqueStrings(out)
}

func actionMatchesPattern(prefixes []string, pattern string) bool {
	for _, pre := range prefixes {
		if pattern == pre || strings.HasPrefix(pattern, pre+" ") {
			return true
		}
	}
	return false
}

func actionUsagePatterns(c *CLI, full string) []string {
	prefixes := actionUsagePrefixes(c, full)
	var colon, space []string
	seen := map[string]bool{}
	add := func(p string, colonForm bool) {
		p = strings.TrimSpace(p)
		if p == "" || seen[p] {
			return
		}
		seen[p] = true
		if colonForm {
			colon = append(colon, p)
			return
		}
		space = append(space, p)
	}
	if expanded := ExpandCLIUsage(c); expanded != "" {
		usage, _, ok := cutUsageBlock(expanded)
		if ok {
			for _, p := range usagePatterns(usage) {
				if !actionMatchesPattern(prefixes, p) {
					continue
				}
				add(p, strings.HasPrefix(p, "flynn "+c.Command+":"))
			}
		}
	}
	if len(colon)+len(space) == 0 {
		for _, p := range synthesizedUsage(c, full) {
			add(p, strings.HasPrefix(p, "flynn "+c.Command+":"))
		}
	}
	return append(colon, space...)
}

func synthesizedUsage(c *CLI, full string) []string {
	a := c.actionByColon(full)
	tail := ""
	if a != nil {
		if s := strings.TrimSpace(a.Append); s != "" {
			tail = " " + s
		}
	}
	colon := "flynn " + full + tail
	space := "flynn " + strings.ReplaceAll(full, ":", " ") + tail
	if a != nil {
		space = "flynn " + c.Command + " " + a.Name + tail
	}
	if colon == space {
		return []string{colon}
	}
	return []string{colon, space}
}

func actionHelpDescription(c *CLI, full string) string {
	descs := clihelp.CommandDescriptions(c.Doc)
	if a := c.actionByColon(full); a != nil {
		if d := descs[a.Name]; d != "" {
			return d
		}
	}
	verb := full
	if i := strings.LastIndex(full, ":"); i >= 0 {
		verb = full[i+1:]
	}
	if d := descs[verb]; d != "" {
		return d
	}
	if full == c.Command || full == c.Command+":list" {
		return strings.TrimSpace(c.Usage)
	}
	return ""
}

func actionHelpNeedles(c *CLI, full string) []string {
	needles := []string{full, "flynn " + full}
	if a := c.actionByColon(full); a != nil {
		space := c.Command + " " + a.Name
		needles = append(needles, space, "flynn "+space)
	} else {
		space := strings.ReplaceAll(full, ":", " ")
		needles = append(needles, space, "flynn "+space)
	}
	return uniqueStrings(needles)
}

func textHasNeedle(s string, needles []string) bool {
	for _, n := range needles {
		if n != "" && strings.Contains(s, n) {
			return true
		}
	}
	return false
}

func actionHelpProse(doc, full string, c *CLI) string {
	needles := actionHelpNeedles(c, full)
	_, rest, ok := cutUsageBlock(doc)
	if !ok {
		rest = doc
	}
	var paras []string
	var cur []string
	flush := func() {
		if len(cur) == 0 {
			return
		}
		p := strings.TrimSpace(strings.Join(cur, " "))
		cur = nil
		if p == "" {
			return
		}
		paras = append(paras, p)
	}
	for _, line := range strings.Split(rest, "\n") {
		trim := strings.TrimSpace(line)
		lower := strings.ToLower(trim)
		if lower == "options:" || lower == "commands:" || lower == "examples:" || lower == "example:" {
			flush()
			break
		}
		if trim == "" {
			flush()
			continue
		}
		cur = append(cur, trim)
	}
	flush()
	desc := strings.TrimSuffix(actionHelpDescription(c, full), ".")
	var out []string
	for _, p := range paras {
		for _, sent := range splitSentences(p) {
			if !textHasNeedle(sent, needles) {
				continue
			}
			if desc != "" && strings.EqualFold(strings.TrimSuffix(sent, "."), desc) {
				continue
			}
			out = append(out, sent)
		}
	}
	return strings.Join(out, " ")
}

func splitSentences(p string) []string {
	p = strings.TrimSpace(p)
	if p == "" {
		return nil
	}
	var out []string
	start := 0
	for i := 0; i < len(p); i++ {
		if p[i] != '.' {
			continue
		}
		if i+1 < len(p) && p[i+1] != ' ' && p[i+1] != '\n' {
			continue
		}
		s := strings.TrimSpace(p[start : i+1])
		if s != "" {
			out = append(out, s)
		}
		start = i + 1
	}
	if tail := strings.TrimSpace(p[start:]); tail != "" {
		out = append(out, tail)
	}
	return out
}

func actionHelpOptions(doc string, patterns []string) string {
	usage := strings.Join(patterns, " ")
	in := false
	var b strings.Builder
	for _, line := range strings.Split(doc, "\n") {
		trim := strings.TrimSpace(line)
		lower := strings.ToLower(trim)
		if lower == "options:" {
			in = true
			continue
		}
		if !in {
			continue
		}
		if trim == "" {
			continue
		}
		if strings.HasSuffix(lower, ":") && !strings.Contains(trim, " ") {
			break
		}
		if optionUsedInUsage(trim, usage) {
			b.WriteString(line)
			if !strings.HasSuffix(line, "\n") {
				b.WriteByte('\n')
			}
		}
	}
	return b.String()
}

func optionUsedInUsage(optionLine, usage string) bool {
	for _, flag := range optionFlags(optionLine) {
		if strings.Contains(usage, flag) {
			return true
		}
	}
	return false
}

func optionFlags(line string) []string {
	trim := strings.TrimSpace(line)
	var flags []string
	for _, tok := range strings.Fields(strings.ReplaceAll(trim, ",", " ")) {
		if !strings.HasPrefix(tok, "-") {
			break
		}
		name := tok
		if i := strings.IndexAny(name, "=<"); i >= 0 {
			name = name[:i]
		}
		if name != "" && name != "-" && name != "--" {
			flags = append(flags, name)
		}
	}
	return flags
}

func actionHelpExamples(doc, full string, c *CLI) string {
	needles := actionHelpNeedles(c, full)
	in := false
	var b strings.Builder
	var block []string
	flush := func() {
		if len(block) == 0 {
			return
		}
		text := strings.Join(block, "\n")
		block = nil
		if !textHasNeedle(text, needles) {
			return
		}
		b.WriteString(text)
		b.WriteByte('\n')
	}
	for _, line := range strings.Split(doc, "\n") {
		trim := strings.TrimSpace(line)
		lower := strings.ToLower(trim)
		if lower == "examples:" || lower == "example:" {
			in = true
			continue
		}
		if !in {
			continue
		}
		if strings.HasSuffix(lower, ":") && !strings.Contains(trim, " ") && lower != "" {
			break
		}
		if strings.HasPrefix(trim, "$") {
			flush()
		}
		if trim == "" && len(block) == 0 {
			continue
		}
		block = append(block, line)
	}
	flush()
	return b.String()
}

func uniqueStrings(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}
