package main

import (
	"testing"

	cfg "github.com/randy-girard/flynn/cli/config"
)

func resetCLIClusterState(t *testing.T) {
	t.Helper()
	prevConfig, prevCluster, prevFlagC, prevFlagA := config, clusterConf, flagCluster, flagApp
	t.Cleanup(func() {
		config, clusterConf, flagCluster, flagApp = prevConfig, prevCluster, prevFlagC, prevFlagA
	})
	config, clusterConf, flagCluster, flagApp = nil, nil, "", ""
}

func testClusters() (cloud, local *cfg.Cluster, conf *cfg.Config) {
	cloud = &cfg.Cluster{
		Name:          "default",
		ControllerURL: "https://controller.nodes.flynn.cloud.example",
		GitURL:        "https://git.nodes.flynn.cloud.example",
	}
	local = &cfg.Cluster{
		Name:          "local",
		ControllerURL: "https://controller.1.localflynn.com",
		GitURL:        "https://git.1.localflynn.com",
	}
	conf = &cfg.Config{Default: "local", Clusters: []*cfg.Cluster{cloud, local}}
	return cloud, local, conf
}

func TestSelectClusterPrefersFlagThenDefaultThenGit(t *testing.T) {
	cloud, local, conf := testClusters()
	got, err := selectCluster(conf, "default", local)
	if err != nil || got != cloud {
		t.Fatalf("flag cluster: got %+v err=%v", got, err)
	}
	got, err = selectCluster(conf, "local", cloud)
	if err != nil || got != local {
		t.Fatalf("named local: got %+v err=%v", got, err)
	}
	got, err = selectCluster(&cfg.Config{Clusters: conf.Clusters}, "", cloud)
	if err != nil || got != cloud {
		t.Fatalf("git fallback: got %+v err=%v", got, err)
	}
	got, err = selectCluster(&cfg.Config{Clusters: conf.Clusters}, "", nil)
	if err != nil || got != cloud {
		t.Fatalf("first cluster: got %+v err=%v", got, err)
	}
	if _, err := selectCluster(conf, "missing", nil); err == nil {
		t.Fatal("missing cluster must error")
	}
	if _, err := selectCluster(nil, "", nil); err != ErrNoClusters {
		t.Fatalf("empty config err=%v", err)
	}
}

func TestGetClusterHonorsFlynnrcDefaultOverGitRemote(t *testing.T) {
	resetCLIClusterState(t)
	cloud, local, conf := testClusters()
	config = conf
	clusterConf = cloud

	got, err := getCluster()
	if err != nil {
		t.Fatal(err)
	}
	if got != local {
		t.Fatalf("default cluster %q, want local (git remote had %q)", got.Name, cloud.Name)
	}
}

func TestGetClusterFlagOverridesDefault(t *testing.T) {
	resetCLIClusterState(t)
	cloud, _, conf := testClusters()
	config = conf
	flagCluster = "default"

	got, err := getCluster()
	if err != nil {
		t.Fatal(err)
	}
	if got != cloud {
		t.Fatalf("got %q, want cloud cluster named default", got.Name)
	}
}

func TestBindGitRemoteAppKeepsDefaultCluster(t *testing.T) {
	resetCLIClusterState(t)
	cloud, local, conf := testClusters()
	config = conf
	clusterConf = local
	bindGitRemoteApp(&remoteApp{Cluster: cloud, Name: "resource-demo"})
	if flagApp != "resource-demo" {
		t.Fatalf("app %q", flagApp)
	}
	if clusterConf != local {
		t.Fatalf("cluster %q, want local", clusterConf.Name)
	}
}

func TestBindGitRemoteAppUsesGitClusterWithoutDefault(t *testing.T) {
	resetCLIClusterState(t)
	cloud, _, conf := testClusters()
	conf.Default = ""
	config = conf
	bindGitRemoteApp(&remoteApp{Cluster: cloud, Name: "resource-demo"})
	if clusterConf != cloud {
		t.Fatalf("cluster %+v", clusterConf)
	}
}

func TestAppFromGitURLFindsAppOnOtherCluster(t *testing.T) {
	resetCLIClusterState(t)
	_, _, conf := testClusters()
	config = conf
	flagCluster = "local"
	ra := appFromGitURL("https://git.nodes.flynn.cloud.example/resource-demo.git")
	if ra == nil || ra.Name != "resource-demo" {
		t.Fatalf("got %+v", ra)
	}
}
