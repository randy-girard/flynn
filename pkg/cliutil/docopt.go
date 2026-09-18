package cliutil

import "github.com/flynn/go-docopt"

// List returns a docopt value as []string. go-docopt stores a single
// `<arg>` as string and `<arg>...` as []string; a missing optional list is
// nil. Callers used to panic with `args.All[k].([]string)` on the string form.
func List(args *docopt.Args, key string) []string {
	if args == nil || args.All == nil {
		return nil
	}
	return listValue(args.All[key])
}

func listValue(v interface{}) []string {
	switch t := v.(type) {
	case []string:
		return t
	case string:
		if t == "" {
			return nil
		}
		return []string{t}
	default:
		return nil
	}
}

// String returns a docopt value as a single string. A repeating argument
// uses the first element.
func String(args *docopt.Args, key string) string {
	if args == nil {
		return ""
	}
	if args.String != nil {
		if s := args.String[key]; s != "" {
			return s
		}
	}
	list := List(args, key)
	if len(list) == 0 {
		return ""
	}
	return list[0]
}
