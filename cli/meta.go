package main

import (
	"fmt"
	"strings"

	"github.com/flynn/go-docopt"
	"github.com/randy-girard/flynn/controller/client"
	"github.com/randy-girard/flynn/controller/types"
	"github.com/randy-girard/flynn/pkg/cliutil"
)

func init() {
	register("meta", runMetaList, `
usage: flynn meta

List metadata for an application.

Examples:

	$ flynn meta
	KEY  VALUE
	foo  bar
`)
	register("meta:set", runMetaSetCmd, `
usage: flynn meta:set <var>=<val>...

Set metadata for an application.

Examples:

	$ flynn meta:set foo=baz bar=qux
`)
	register("meta:unset", runMetaUnsetCmd, `
usage: flynn meta:unset <var>...

Unset metadata for an application.

Examples:

	$ flynn meta:unset foo
`)
}

func runMetaList(args *docopt.Args, client controller.Client) error {
	app, err := client.GetApp(mustApp())
	if err != nil {
		return err
	}
	return runMetaGet(app, args, client)
}

func runMetaSetCmd(args *docopt.Args, client controller.Client) error {
	app, err := client.GetApp(mustApp())
	if err != nil {
		return err
	}
	return runMetaSet(app, args, client)
}

func runMetaUnsetCmd(args *docopt.Args, client controller.Client) error {
	app, err := client.GetApp(mustApp())
	if err != nil {
		return err
	}
	return runMetaUnset(app, args, client)
}

func runMetaGet(app *types.App, args *docopt.Args, client controller.Client) error {
	w := tabWriter()
	defer w.Flush()
	listRec(w, "KEY", "VALUE")
	for k, v := range app.Meta {
		listRec(w, k, v)
	}
	return nil
}

func runMetaSet(app *types.App, args *docopt.Args, client controller.Client) error {
	pairs := cliutil.List(args, "<var>=<val>")
	if app.Meta == nil {
		app.Meta = make(map[string]string, len(pairs))
	}
	for _, s := range pairs {
		v := strings.SplitN(s, "=", 2)
		if len(v) != 2 {
			return fmt.Errorf("invalid var format: %q", s)
		}
		app.Meta[v[0]] = v[1]
	}
	return client.UpdateAppMeta(app)
}

func runMetaUnset(app *types.App, args *docopt.Args, client controller.Client) error {
	vars := cliutil.List(args, "<var>")
	for _, s := range vars {
		delete(app.Meta, s)
	}
	return client.UpdateAppMeta(app)
}
