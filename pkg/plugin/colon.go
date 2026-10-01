package plugin

import "strings"

// ColonName maps a plugin action to the canonical flynn <command>:<suffix>
// name. Two tokens become a nested noun:verb (kafka:topics:create). A
// hyphenated noun stays hyphenated (kafka:consumer-groups:create).
// Three-or-more-token actions stay hyphenated verbs (disable-system-routes).
func ColonName(command, actionName string) string {
	parts := strings.Fields(actionName)
	suffix := strings.Join(parts, "-")
	if len(parts) == 2 {
		suffix = parts[0] + ":" + parts[1]
	}
	switch {
	case command == "redis" && actionName == "redis-cli":
		suffix = "cli"
	case command == "mysql" && actionName == "console":
		suffix = "cli"
	case command == "mongodb" && actionName == "mongo":
		suffix = "cli"
	case command == "clickhouse" && actionName == "client":
		suffix = "cli"
	}
	if command == "" {
		return suffix
	}
	if suffix == "" {
		return command
	}
	return command + ":" + suffix
}
