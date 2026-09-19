package main

import (
	"fmt"
	"os"
	"strings"
)

// topAliases rewrites a single-token command (flynn create → apps:create).
var topAliases = map[string]string{
	"create":         "apps:create",
	"delete":         "apps:destroy",
	"info":           "apps:info",
	"kill":           "ps:kill",
	"export":         "apps:export",
	"import":         "apps:import",
	"plugins":        "plugin:list",
	"deployment":     "deploy",
	"logsink":        "log-sink",
	"logsink:add":    "log-sink:add",
	"logsink:remove": "log-sink:remove",
}

// subAliases rewrites flynn <noun> <verb> to flynn <noun>:<verb>.
var subAliases = map[string]map[string]string{
	"apps": {
		"create":  "apps:create",
		"destroy": "apps:destroy",
		"info":    "apps:info",
		"export":  "apps:export",
		"import":  "apps:import",
	},
	"alert": {
		"add":     "alert:add",
		"enable":  "alert:enable",
		"disable": "alert:disable",
		"remove":  "alert:remove",
	},
	"env": {
		"set":   "env:set",
		"unset": "env:unset",
		"get":   "env:get",
	},
	"meta": {
		"set":   "meta:set",
		"unset": "meta:unset",
	},
	"limit": {
		"set":      "limit:set",
		"profile":  "limit:profile",
		"profiles": "limit:profiles",
	},
	"ps": {
		"kill":  "ps:kill",
		"scale": "ps:scale",
		"run":   "ps:run",
	},
	"release": {
		"add":      "release:add",
		"update":   "release:update",
		"show":     "release:show",
		"delete":   "release:destroy",
		"destroy":  "release:destroy",
		"rollback": "release:rollback",
	},
	"deploy": {
		"timeout":    "deploy:timeout",
		"batch-size": "deploy:batch-size",
	},
	"deployment": {
		"timeout":    "deploy:timeout",
		"batch-size": "deploy:batch-size",
	},
	"stack": {
		"set": "stack:set",
	},
	"route": {
		"add":    "route:add",
		"update": "route:update",
		"remove": "route:remove",
	},
	"resource": {
		"add":      "resource:add",
		"remove":   "resource:remove",
		"expose":   "resource:expose",
		"unexpose": "resource:unexpose",
	},
	"provider": {
		"add": "provider:add",
	},
	"volume": {
		"show":         "volume:show",
		"decommission": "volume:decommission",
	},
	"cluster": {
		"add":            "cluster:add",
		"remove":         "cluster:remove",
		"default":        "cluster:default",
		"update-pin":     "cluster:refresh",
		"refresh":        "cluster:refresh",
		"backup":         "cluster:backup",
		"log-sink":       "cluster:log-sink",
		"migrate-domain": "cluster:migrate-domain",
	},
	"docker": {
		"push":         "docker:push",
		"set-push-url": "docker:set-push-url",
		"login":        "docker:login",
		"logout":       "docker:logout",
	},
	"github": {
		"connect":    "github:connect",
		"disconnect": "github:disconnect",
		"deploy":     "github:deploy",
		"set":        "github:set",
	},
	"pg": {
		"psql":    "pg:psql",
		"dump":    "pg:dump",
		"restore": "pg:restore",
	},
	"remote": {
		"add": "git:remote",
	},
	"plugin": {
		"list": "plugin:list",
	},
	"log-sink": {
		"add":    "log-sink:add",
		"remove": "log-sink:remove",
	},
	"logsink": {
		"add":    "log-sink:add",
		"remove": "log-sink:remove",
	},
}

// movedToHost is printed for commands that now live on flynn-host. The old
// flynn implementation still runs for one release.
var movedToHost = map[string]string{
	"cluster:backup":         "flynn-host backup",
	"cluster:migrate-domain": "flynn-host migrate-domain",
	"cluster:log-sink":       "flynn-host log-sink",
}

func resolveCommand(name string, args []string) (string, []string, string) {
	if name == "" {
		return name, args, ""
	}
	if len(args) > 0 {
		if subs, ok := subAliases[name]; ok {
			if target, ok := subs[args[0]]; ok {
				from := name + " " + args[0]
				return target, args[1:], from
			}
		}
	}
	if target, ok := topAliases[name]; ok {
		return target, args, name
	}
	return name, args, ""
}

func printCommandRename(from, to string) {
	if from == "" || from == to {
		return
	}
	if host, ok := movedToHost[to]; ok {
		fmt.Fprintf(os.Stderr, "flynn %s has moved to %s (still running the old command this release)\n", from, host)
		return
	}
	fmt.Fprintf(os.Stderr, "flynn %s is now flynn %s\n", from, to)
}

func splitColonCommand(name string) (base, suffix string, ok bool) {
	i := strings.IndexByte(name, ':')
	if i <= 0 || i == len(name)-1 {
		return "", "", false
	}
	return name[:i], name[i+1:], true
}

// pluginColonName maps a plugin action to a colon command.
// Two tokens become a nested noun:verb (kafka:topics:create). A hyphenated
// noun stays hyphenated (kafka:consumer-groups:create). Three-or-more-token
// actions stay hyphenated verbs (disable-system-routes).
func pluginColonName(command, actionName string) string {
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
	case command == "pg" && actionName == "psql":
		suffix = "cli"
	}
	return command + ":" + suffix
}

func pluginSpaceAlias(name string, args []string) (to, from string) {
	if len(args) == 0 {
		return "", ""
	}
	cat, err := clusterPluginCatalog()
	if err != nil || cat == nil {
		return "", ""
	}
	spec := cat.Lookup(name)
	if spec == nil {
		return "", ""
	}
	bestN := 0
	bestName := ""
	for _, a := range spec.Actions {
		parts := strings.Fields(a.Name)
		if len(parts) == 0 || len(args) < len(parts) {
			continue
		}
		ok := true
		for i, p := range parts {
			if args[i] != p {
				ok = false
				break
			}
		}
		if ok && len(parts) > bestN {
			bestN = len(parts)
			bestName = a.Name
		}
	}
	if bestN == 0 {
		return "", ""
	}
	return pluginColonName(name, bestName), name + " " + strings.Join(args[:bestN], " ")
}

func pluginColonRename(name string) (to, from string) {
	base, suffix, ok := splitColonCommand(name)
	if !ok || strings.Contains(suffix, ":") || !strings.Contains(suffix, "-") {
		return "", ""
	}
	cat, err := clusterPluginCatalog()
	if err != nil || cat == nil {
		return "", ""
	}
	spec := cat.Lookup(base)
	if spec == nil {
		return "", ""
	}
	for _, a := range spec.Actions {
		if strings.Join(strings.Fields(a.Name), "-") != suffix {
			continue
		}
		canonical := pluginColonName(base, a.Name)
		if canonical != name {
			return canonical, name
		}
	}
	return "", ""
}

// expandColonSuffix turns redis:cli / kafka:topics:create into plugin action tokens.
// Hyphen form kafka:topics-create remains an alias.
func expandColonSuffix(plugin, suffix string) []string {
	if suffix == "cli" {
		switch plugin {
		case "redis":
			return []string{"redis-cli"}
		case "mysql":
			return []string{"console"}
		case "mongodb":
			return []string{"mongo"}
		case "clickhouse":
			return []string{"client"}
		case "pg":
			return []string{"psql"}
		default:
			return []string{"cli"}
		}
	}
	if strings.Contains(suffix, ":") {
		return strings.Split(suffix, ":")
	}
	return strings.Split(suffix, "-")
}
