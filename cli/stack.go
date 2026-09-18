package main

import (
	"fmt"
	"log"

	"github.com/flynn/flynn/controller/client"
	"github.com/flynn/go-docopt"
)

const (
	stackHeroku24  = "heroku-24"
	stackContainer = "container"
)

func init() {
	register("stack", runStackShow, `
usage: flynn stack

Show the deployment stack for git push deploys.

Stacks:
  heroku-24  Build apps with buildpacks (default)
  container  Build apps from a Dockerfile on the server using BuildKit

Examples:

	$ flynn stack
	heroku-24
`)
	register("stack:set", runStackSet, `
usage: flynn stack:set <stack>

Set the deployment stack for git push deploys.

Stacks:
  heroku-24  Build apps with buildpacks (default)
  container  Build apps from a Dockerfile on the server using BuildKit

Examples:

	$ flynn stack:set container
	Created release 5058ae7964f74c399a240bdd6e7d1bcb.
`)
}

func runStackShow(_ *docopt.Args, client controller.Client) error {
	release, err := client.GetAppRelease(mustApp())
	if err == controller.ErrNotFound {
		fmt.Println(stackHeroku24)
		return nil
	}
	if err != nil {
		return err
	}
	stack := release.Env["FLYNN_STACK"]
	if stack == "" {
		stack = stackHeroku24
	}
	fmt.Println(stack)
	return nil
}

func runStackSet(args *docopt.Args, client controller.Client) error {
	stack := args.String["<stack>"]
	switch stack {
	case stackHeroku24, stackContainer:
	default:
		return fmt.Errorf("unknown stack %q (valid stacks: %s, %s)", stack, stackHeroku24, stackContainer)
	}

	env := map[string]*string{"FLYNN_STACK": &stack}
	if stack == stackHeroku24 {
		env["FLYNN_STACK"] = nil
	}
	id, err := setEnv(client, "", env)
	if err != nil {
		return err
	}
	log.Printf("Created release %s.", id)
	return nil
}
