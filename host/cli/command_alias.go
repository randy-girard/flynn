package cli

import (
	"fmt"
	"os"
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
		"install":   "plugin:install",
		"update":    "plugin:update",
		"uninstall": "plugin:uninstall",
		"list":      "plugin:list",
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
	"runtime-profile": {
		"create":       "runtime-profile:create",
		"update":       "runtime-profile:update",
		"remove":       "runtime-profile:remove",
		"delete":       "runtime-profile:remove",
		"allow-custom": "runtime-profile:allow-custom",
	},
	"route": {
		"add": "route:add",
	},
}

// ResolveCommand rewrites space-nested flynn-host commands to colon names.
func ResolveCommand(name string, args []string) (string, []string, string) {
	if name == "" {
		return name, args, ""
	}
	if name == "plugin" && len(args) >= 2 && args[0] == "credentials" {
		switch args[1] {
		case "set", "unset", "show":
			return "plugin:credentials-" + args[1], args[2:], "plugin credentials " + args[1]
		}
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
	if target, ok := topAliases[name]; ok && target != name {
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
