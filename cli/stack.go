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
	register("stack", runStack, `
usage: flynn stack
       flynn stack set <stack>

Manage the deployment stack for git push deploys.

Stacks:
  heroku-24  Build apps with buildpacks (default)
  container  Build apps from a Dockerfile on the server using BuildKit

Examples:

	$ flynn stack
	heroku-24

	$ flynn stack set container
	Created release 5058ae7964f74c399a240bdd6e7d1bcb.
`)
}

func runStack(args *docopt.Args, client controller.Client) error {
	if args.Bool["set"] {
		return runStackSet(args, client)
	}
	return runStackShow(client)
}

func runStackShow(client controller.Client) error {
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
