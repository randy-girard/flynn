package main

import (
	"fmt"

	controller "github.com/flynn/flynn/controller/client"
	ct "github.com/flynn/flynn/controller/types"
)

// MySQL console/dump/restore live on flynn-plugin-mariadb. These helpers remain
// so `flynn export` / `flynn import` can dump and restore a provisioned database
// without a compiled `flynn mysql` command.

func getAppMysqlRunConfig(client controller.Client) (*runConfig, error) {
	appRelease, err := client.GetAppRelease(mustApp())
	if err != nil {
		return nil, fmt.Errorf("error getting app release: %s", err)
	}
	return getMysqlRunConfig(client, mustApp(), appRelease)
}

func getMysqlRunConfig(client controller.Client, appName string, appRelease *ct.Release) (*runConfig, error) {
	app := appRelease.Env["FLYNN_MYSQL"]
	if app == "" {
		return nil, fmt.Errorf("No mysql database found. Provision one with `flynn resource add mysql`")
	}

	release, err := client.GetAppRelease(app)
	if err != nil {
		return nil, fmt.Errorf("error getting mysql release: %s", err)
	}

	if appRelease.Env["MYSQL_USER"] == "" {
		return nil, fmt.Errorf("missing MYSQL_USER in app environment")
	}

	config := &runConfig{
		App:        appName,
		Release:    release.ID,
		Env:        make(map[string]string),
		DisableLog: true,
		Exit:       true,
	}

	for _, k := range []string{"MYSQL_HOST", "MYSQL_USER", "MYSQL_PWD", "MYSQL_DATABASE"} {
		v := appRelease.Env[k]
		if v == "" {
			return nil, fmt.Errorf("missing %s in app environment", k)
		}
		config.Env[k] = v
	}
	return config, nil
}

func configMysqlDump(config *runConfig) {
	config.Args = []string{
		"mysqldump",
		"-h", config.Env["MYSQL_HOST"],
		"-u", config.Env["MYSQL_USER"],
		config.Env["MYSQL_DATABASE"],
	}
}

func mysqlRestore(client controller.Client, config *runConfig) error {
	config.Args = []string{"mysql", "-u", config.Env["MYSQL_USER"], "-D", config.Env["MYSQL_DATABASE"]}
	err := runJob(client, *config)
	if exit, ok := err.(RunExitError); ok && exit == 1 {
		return nil
	}
	return err
}
