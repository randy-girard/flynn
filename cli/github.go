package main

import (
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"text/tabwriter"

	"github.com/flynn/go-docopt"
	controller "github.com/randy-girard/flynn/controller/client"
	ct "github.com/randy-girard/flynn/controller/types"
	"github.com/randy-girard/flynn/pkg/githubapp"
)

func init() {
	register("github", runGitHubShow, `
usage: flynn github

Show the GitHub repository connected to this app.
`)
	register("github:connect", runGitHubConnect, `
usage: flynn github:connect [--installation=<id>] [--branch=<branch>] [--auto-deploy] [--wait-checks] <owner/repo>

Connect a GitHub repository to this app. The cluster GitHub App must already
be configured (flynn-host github:configure). Default branch is the repo
default (main or master).

Options:
    --installation=<id>  GitHub App installation id (default: find one that can access the repo)
    --branch=<branch>    Branch to deploy (default: repository default)
    --auto-deploy        Deploy this branch on GitHub push
    --wait-checks        Wait for GitHub Checks / commit statuses before auto-deploy
`)
	register("github:disconnect", runGitHubDisconnect, `
usage: flynn github:disconnect

Disconnect the GitHub repository from this app.
`)
	register("github:deploy", runGitHubDeploy, `
usage: flynn github:deploy [--branch=<branch>]

Deploy the connected GitHub repository (default branch, or --branch).

Options:
    --branch=<branch>  Branch to deploy
`)
	register("github:set", runGitHubSet, `
usage: flynn github:set [--branch=<branch>] [--auto-deploy=<on|off>] [--wait-checks=<on|off>]

Update GitHub deploy settings for this app.

Options:
    --branch=<branch>           Deploy this branch
    --auto-deploy=<on|off>      Enable or disable push auto-deploys
    --wait-checks=<on|off>      Wait for GitHub Checks before auto-deploy
`)
}

type gitHubAppAPI interface {
	GetAppGitHub(appID string) (*ct.GitHubRepoConnection, error)
	PutAppGitHub(appID string, conn *ct.GitHubRepoConnection) error
	DeleteAppGitHub(appID string) error
	DeployAppGitHub(appID string, req *ct.GitHubDeployRequest) (*ct.GitHubDeploy, error)
}

func runGitHubShow(args *docopt.Args, client controller.Client) error {
	return printGitHubConnection(os.Stdout, client, mustApp())
}

func runGitHubConnect(args *docopt.Args, client controller.Client) error {
	return connectGitHub(args, client, mustApp())
}

func runGitHubDisconnect(args *docopt.Args, client controller.Client) error {
	return client.DeleteAppGitHub(mustApp())
}

func runGitHubDeploy(args *docopt.Args, client controller.Client) error {
	dep, err := client.DeployAppGitHub(mustApp(), &ct.GitHubDeployRequest{Branch: args.String["--branch"]})
	if err != nil {
		return err
	}
	fmt.Printf("Deploying %s/%s@%s (%s) via taffy job %s\n", dep.Owner, dep.Repo, dep.Branch, dep.SHA, dep.JobID)
	return nil
}

func runGitHubSet(args *docopt.Args, client controller.Client) error {
	conn, err := client.GetAppGitHub(mustApp())
	if err != nil {
		return err
	}
	if b := args.String["--branch"]; b != "" {
		conn.Branch = b
	}
	if v, ok := parseOnOff(args.String["--auto-deploy"]); ok {
		conn.AutoDeploy = v
	}
	if v, ok := parseOnOff(args.String["--wait-checks"]); ok {
		conn.WaitForChecks = v
	}
	if err := client.PutAppGitHub(mustApp(), conn); err != nil {
		return err
	}
	return printGitHubConnection(os.Stdout, client, mustApp())
}

func connectGitHub(args *docopt.Args, client gitHubAppAPI, appID string) error {
	owner, repo, err := githubapp.SplitOwnerRepo(args.String["<owner/repo>"])
	if err != nil {
		return err
	}
	conn := &ct.GitHubRepoConnection{
		Owner:         owner,
		Repo:          repo,
		Branch:        args.String["--branch"],
		AutoDeploy:    args.Bool["--auto-deploy"],
		WaitForChecks: args.Bool["--wait-checks"],
	}
	if raw := args.String["--installation"]; raw != "" {
		id, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			return fmt.Errorf("--installation must be an integer")
		}
		conn.InstallationID = id
	}
	if err := client.PutAppGitHub(appID, conn); err != nil {
		return err
	}
	got, err := client.GetAppGitHub(appID)
	if err != nil {
		return err
	}
	return printGitHubConn(os.Stdout, got)
}

func printGitHubConnection(w io.Writer, client gitHubAppAPI, appID string) error {
	conn, err := client.GetAppGitHub(appID)
	if err != nil {
		return err
	}
	return printGitHubConn(w, conn)
}

func printGitHubConn(w io.Writer, conn *ct.GitHubRepoConnection) error {
	tw := tabwriter.NewWriter(w, 0, 8, 2, ' ', 0)
	fmt.Fprintf(tw, "REPO\t%s\n", conn.Owner+"/"+conn.Repo)
	fmt.Fprintf(tw, "ACCOUNT\t%s\n", conn.AccountLogin)
	fmt.Fprintf(tw, "BRANCH\t%s\n", conn.Branch)
	fmt.Fprintf(tw, "AUTO DEPLOY\t%v\n", conn.AutoDeploy)
	fmt.Fprintf(tw, "WAIT FOR CHECKS\t%v\n", conn.WaitForChecks)
	if conn.LastDeploySHA != "" {
		fmt.Fprintf(tw, "LAST DEPLOY\t%s\n", conn.LastDeploySHA)
	}
	return tw.Flush()
}

func parseOnOff(raw string) (bool, bool) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "on", "true", "1", "yes":
		return true, true
	case "off", "false", "0", "no":
		return false, true
	default:
		return false, false
	}
}
