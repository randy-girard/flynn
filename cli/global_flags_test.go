package main

import (
	"strings"
	"testing"

	"github.com/flynn/go-docopt"
	cfg "github.com/randy-girard/flynn/cli/config"
)

func parseRoot(t *testing.T, argv []string) *docopt.Args {
	t.Helper()
	args, err := docopt.Parse(cliUsage, argv, false, "", true, false)
	if err != nil {
		t.Fatalf("parse %q: %v", argv, err)
	}
	return args
}

func stubGitRemote(t *testing.T, remotes map[string]*remoteApp) {
	t.Helper()
	prev := lookupGitRemoteApp
	t.Cleanup(func() { lookupGitRemoteApp = prev })
	lookupGitRemoteApp = func(remote string) (*remoteApp, error) {
		if remote == "" {
			return appFromGitRemote("")
		}
		if ra, ok := remotes[remote]; ok {
			return ra, nil
		}
		return nil, nil
	}
}

func TestGlobalFlagsStayBeforeTheCommand(t *testing.T) {
	cases := []struct {
		argv           []string
		cmd            string
		args           string
		app, remote, c string
	}{
		{[]string{"-a", "shop", "ps"}, "ps", "", "shop", "", ""},
		{[]string{"ps", "-a"}, "ps", "-a", "", "", ""},
		{[]string{"-r", "staging", "scale"}, "scale", "", "", "staging", ""},
		{[]string{"scale", "-r", "rel"}, "scale", "-r rel", "", "", ""},
		{[]string{"log", "-r"}, "log", "-r", "", "", ""},
		{[]string{"apps:create", "-r", "staging"}, "apps:create", "-r staging", "", "", ""},
		{[]string{"-a", "shop", "-r", "origin", "-c", "prod", "ps"}, "ps", "", "shop", "origin", "prod"},
		{[]string{"-r", "staging", "apps:destroy", "-r", "staging"}, "apps:destroy", "-r staging", "", "staging", ""},
	}
	for _, tc := range cases {
		args := parseRoot(t, tc.argv)
		cmd, cmdArgs := positionalArgs(args)
		if cmd != tc.cmd || strings.Join(cmdArgs, " ") != tc.args {
			t.Fatalf("%q: cmd=%q args=%q want %q %q", tc.argv, cmd, cmdArgs, tc.cmd, tc.args)
		}
		if args.String["-a"] != tc.app || args.String["-r"] != tc.remote || args.String["-c"] != tc.c {
			t.Fatalf("%q: -a=%q -r=%q -c=%q want %q %q %q", tc.argv, args.String["-a"], args.String["-r"], args.String["-c"], tc.app, tc.remote, tc.c)
		}
	}
}

func TestApplyGlobalFlagsRemoteAndApp(t *testing.T) {
	resetCLIClusterState(t)
	_, local, conf := testClusters()
	config = conf
	shop := &remoteApp{Cluster: local, Name: "shop"}
	stubGitRemote(t, map[string]*remoteApp{"staging": shop})

	if err := applyGlobalFlags(parseRoot(t, []string{"-r", "staging", "ps"})); err != nil {
		t.Fatal(err)
	}
	if flagRemote != "staging" || flagApp != "shop" {
		t.Fatalf("flagRemote=%q flagApp=%q", flagRemote, flagApp)
	}
	got, err := getCluster()
	if err != nil || got != local {
		t.Fatalf("cluster %+v err=%v", got, err)
	}

	resetCLIClusterState(t)
	config = conf
	stubGitRemote(t, map[string]*remoteApp{"staging": shop})
	if err := applyGlobalFlags(parseRoot(t, []string{"-a", "shop", "-r", "staging", "ps"})); err != nil {
		t.Fatal(err)
	}
	if flagApp != "shop" {
		t.Fatalf("agreeing -a/-r: %q", flagApp)
	}

	resetCLIClusterState(t)
	config = conf
	stubGitRemote(t, map[string]*remoteApp{"staging": shop})
	err = applyGlobalFlags(parseRoot(t, []string{"-a", "other", "-r", "staging", "ps"}))
	if err == nil || !strings.Contains(err.Error(), `git remote "staging" is app "shop", not "other"`) {
		t.Fatalf("conflict: %v", err)
	}
}

func TestApplyGlobalFlagsRemoteClusterMismatch(t *testing.T) {
	resetCLIClusterState(t)
	cloud, local, conf := testClusters()
	config = conf
	stubGitRemote(t, map[string]*remoteApp{"cloudapp": {Cluster: cloud, Name: "resource-demo"}})
	err := applyGlobalFlags(parseRoot(t, []string{"-r", "cloudapp", "-c", "local", "ps"}))
	if err == nil || !strings.Contains(err.Error(), "cluster") {
		t.Fatalf("mismatch: %v", err)
	}
	_ = local
}

func TestAppPrecedenceFlagsBeatEnv(t *testing.T) {
	resetCLIClusterState(t)
	_, local, conf := testClusters()
	config = conf
	t.Setenv("FLYNN_APP", "from-env")
	t.Setenv("FLYNN_REMOTE", "staging")
	flagApp = "from-flag"
	got, err := app()
	if err != nil || got != "from-flag" {
		t.Fatalf("got %q err=%v", got, err)
	}

	resetCLIClusterState(t)
	config = conf
	t.Setenv("FLYNN_APP", "from-env")
	t.Setenv("FLYNN_REMOTE", "")
	stubGitRemote(t, map[string]*remoteApp{"staging": {Cluster: local, Name: "shop"}})
	flagRemote = "staging"
	got, err = app()
	if err != nil || got != "shop" {
		t.Fatalf("-r beats FLYNN_APP: got %q err=%v", got, err)
	}
}

func TestAppHonorsFLYNN_REMOTE(t *testing.T) {
	resetCLIClusterState(t)
	_, local, conf := testClusters()
	config = conf
	t.Setenv("FLYNN_APP", "")
	t.Setenv("FLYNN_REMOTE", "staging")
	stubGitRemote(t, map[string]*remoteApp{"staging": {Cluster: local, Name: "shop"}})
	got, err := app()
	if err != nil || got != "shop" {
		t.Fatalf("got %q err=%v", got, err)
	}
}

func TestAppFLYNNAppAndRemoteMustAgree(t *testing.T) {
	resetCLIClusterState(t)
	_, local, conf := testClusters()
	config = conf
	t.Setenv("FLYNN_APP", "other")
	t.Setenv("FLYNN_REMOTE", "staging")
	stubGitRemote(t, map[string]*remoteApp{"staging": {Cluster: local, Name: "shop"}})
	_, err := app()
	if err == nil || !strings.Contains(err.Error(), `git remote "staging" is app "shop", not "other"`) {
		t.Fatalf("got %v", err)
	}
}

func TestAppFLYNNAppBeatsFLYNNRemoteWhenTheyAgree(t *testing.T) {
	resetCLIClusterState(t)
	_, local, conf := testClusters()
	config = conf
	t.Setenv("FLYNN_APP", "shop")
	t.Setenv("FLYNN_REMOTE", "staging")
	stubGitRemote(t, map[string]*remoteApp{"staging": {Cluster: local, Name: "shop"}})
	got, err := app()
	if err != nil || got != "shop" {
		t.Fatalf("got %q err=%v", got, err)
	}
}

func TestGlobalFlagsParseForEveryRegisteredCommand(t *testing.T) {
	if len(commands) < 10 {
		t.Fatalf("expected registered commands, got %d", len(commands))
	}
	for name := range commands {
		args := parseRoot(t, []string{"-a", "demo", "-r", "origin", "-c", "local", name})
		cmd, cmdArgs := positionalArgs(args)
		if cmd != name {
			t.Fatalf("%s: command %q", name, cmd)
		}
		if args.String["-a"] != "demo" || args.String["-r"] != "origin" || args.String["-c"] != "local" {
			t.Fatalf("%s: global flags not parsed before command: %v %v", name, args.String, cmdArgs)
		}
		if len(cmdArgs) != 0 {
			t.Fatalf("%s: extra args %q", name, cmdArgs)
		}
	}
}

func TestApplyGlobalFlagsRemoteWithoutConfigIsOKForMissingRemoteLookup(t *testing.T) {
	resetCLIClusterState(t)
	config = &cfg.Config{}
	stubGitRemote(t, nil)
	if err := applyGlobalFlags(parseRoot(t, []string{"-a", "demo", "ps"})); err != nil {
		t.Fatal(err)
	}
	if flagApp != "demo" {
		t.Fatalf("app name %q", flagApp)
	}
}
