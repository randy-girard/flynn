package cli

import (
	"fmt"
	"os"
	"strings"
)

// topAliases rewrites a single-token command (flynn-host plugin → plugin:list).
var topAliases = map[string]string{
	"plugin":          "plugin:list",
	"volume":          "volume:list",
	"logsink":         "log-sink",
	"logsink:add":     "log-sink:add",
	"logsink:list":    "log-sink:list",
	"logsink:remove":  "log-sink:remove",
	"runtime-profile": "runtime-profile",
}

// hyphenAliases rewrites nested-noun hyphen names to extra-colon canonical names.
// Hyphenated verbs (plugin:update-all, acme:disable-system-routes) stay as-is.
var hyphenAliases = map[string]string{
	"plugin:credentials-set":   "plugin:credentials:set",
	"plugin:credentials-unset": "plugin:credentials:unset",
	"plugin:credentials-show":  "plugin:credentials:show",
	"firewall:peer-add":        "firewall:peer:add",
	"firewall:peer-remove":     "firewall:peer:remove",
}

// subAliases rewrites flynn-host <noun> <verb> to flynn-host <noun>:<verb>.
var subAliases = map[string]map[string]string{
	"volume": {
		"list":    "volume:list",
		"create":  "volume:create",
		"delete":  "volume:delete",
		"destroy": "volume:delete",
		"gc":      "volume:gc",
	},
	"log-sink": {
		"list":   "log-sink:list",
		"add":    "log-sink:add",
		"remove": "log-sink:remove",
	},
	"logsink": {
		"list":   "log-sink:list",
		"add":    "log-sink:add",
		"remove": "log-sink:remove",
	},
	"otel": {
		"add":    "otel:add",
		"remove": "otel:remove",
	},
	"domain": {
		"apex": "domain:apex",
	},
	"plugin": {
		"install":    "plugin:install",
		"update":     "plugin:update",
		"update-all": "plugin:update-all",
		"uninstall":  "plugin:uninstall",
		"list":       "plugin:list",
	},
	"plugin:credentials": {
		"set":   "plugin:credentials:set",
		"unset": "plugin:credentials:unset",
		"show":  "plugin:credentials:show",
	},
	"tags": {
		"set": "tags:set",
		"del": "tags:del",
	},
	"webhooks": {
		"add":    "webhooks:add",
		"remove": "webhooks:remove",
	},
	"acme": {
		"configure":             "acme:configure",
		"enable":                "acme:enable",
		"disable":               "acme:disable",
		"status":                "acme:status",
		"enable-system-routes":  "acme:enable-system-routes",
		"disable-system-routes": "acme:disable-system-routes",
	},
	"github": {
		"configure": "github:configure",
		"status":    "github:status",
		"setup":     "github:setup",
		"disable":   "github:disable",
	},
	"runtime-profile": {
		"create":       "runtime-profile:create",
		"update":       "runtime-profile:update",
		"remove":       "runtime-profile:remove",
		"delete":       "runtime-profile:remove",
		"allow-custom": "runtime-profile:allow-custom",
	},
	"events": {
		"visible": "events:visible",
	},
	"alert": {
		"add":     "alert:add",
		"enable":  "alert:enable",
		"disable": "alert:disable",
		"remove":  "alert:remove",
	},
	"route": {
		"add": "route:add",
	},
	"firewall": {
		"sync":        "firewall:sync",
		"peer-add":    "firewall:peer:add",
		"peer-remove": "firewall:peer:remove",
		"expose":      "firewall:expose",
		"unexpose":    "firewall:unexpose",
	},
	"firewall:peer": {
		"add":    "firewall:peer:add",
		"remove": "firewall:peer:remove",
	},
}

func aliasUsage(canonical, alias, usage string) string {
	return strings.ReplaceAll(usage, "flynn-host "+canonical, "flynn-host "+alias)
}

// ResolveCommand rewrites space-nested and hyphen-nested flynn-host commands to colon names.
func ResolveCommand(name string, args []string) (string, []string, string) {
	if name == "" {
		return name, args, ""
	}
	if target, ok := hyphenAliases[name]; ok {
		return target, args, name
	}
	if len(args) >= 2 {
		if subs, ok := subAliases[name+":"+args[0]]; ok {
			if target, ok := subs[args[1]]; ok {
				return target, args[2:], name + " " + args[0] + " " + args[1]
			}
		}
	}
	if name == "plugin" && len(args) >= 1 && args[0] == "credentials" {
		return "plugin:credentials", args[1:], "plugin credentials"
	}
	if name == "plugin" && len(args) >= 2 && args[1] == "route" {
		pluginName := args[0]
		rest := append([]string{pluginName}, args[2:]...)
		return "plugin:route", rest, "plugin " + pluginName + " route"
	}
	if len(args) > 0 {
		if subs, ok := subAliases[name]; ok {
			if target, ok := subs[args[0]]; ok {
				return target, args[1:], name + " " + args[0]
			}
		}
	}
	if target, ok := topAliases[name]; ok && target != name && helpOnlyArgs(args) {
		return target, args, name
	}
	return name, args, ""
}

func PrintCommandRename(from, to string) {
	if from == "" || from == to {
		return
	}
	fmt.Fprintf(os.Stderr, "flynn-host %s is now flynn-host %s\n", from, to)
}
