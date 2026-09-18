package main

import (
	"fmt"
	"strconv"

	controller "github.com/flynn/flynn/controller/client"
	ct "github.com/flynn/flynn/controller/types"
	"github.com/flynn/go-docopt"
)

func init() {
	register("deploy", runDeployList, `
usage: flynn deploy

List app deployments.

Examples:

	$ flynn deploy
	ID                                    STATUS    CREATED             FINISHED
	a6d470d6-9638-4d74-ae71-91c3d9887714  running   4 seconds ago
	39f8b98b-2aed-40a5-9423-ae174b3fb7a9  complete  16 seconds ago      14 seconds ago
`)
	register("deploy:timeout", runDeployTimeout, `
usage: flynn deploy:timeout [<timeout>]

Get or set the number of seconds to wait for each job to start when deploying.

Examples:

	$ flynn deploy:timeout 150

	$ flynn deploy:timeout
	150
`)
	register("deploy:batch-size", runDeployBatchSize, `
usage: flynn deploy:batch-size [<size>]

Get or set the batch size for deployments using the in-batches strategy.

Examples:

	$ flynn deploy:batch-size 3

	$ flynn deploy:batch-size
	3
`)
}

func runDeployList(_ *docopt.Args, client controller.Client) error {
	deployments, err := client.DeploymentList(mustApp())
	if err != nil {
		return err
	}

	w := tabWriter()
	defer w.Flush()

	listRec(w, "ID", "STATUS", "CREATED", "FINISHED")
	for _, d := range deployments {
		listRec(w, d.ID, d.Status, humanTime(d.CreatedAt), humanTime(d.FinishedAt))
	}
	return nil
}

func runDeployTimeout(args *docopt.Args, client controller.Client) error {
	if args.String["<timeout>"] != "" {
		return runSetDeployTimeout(args, client)
	}
	return runGetDeployTimeout(args, client)
}

func runDeployBatchSize(args *docopt.Args, client controller.Client) error {
	if args.String["<size>"] != "" {
		return runSetDeployBatchSize(args, client)
	}
	return runGetDeployBatchSize(args, client)
}

func runGetDeployTimeout(args *docopt.Args, client controller.Client) error {
	app, err := client.GetApp(mustApp())
	if err != nil {
		return err
	}
	fmt.Println(app.DeployTimeout)
	return nil
}

func runSetDeployTimeout(args *docopt.Args, client controller.Client) error {
	timeout, err := strconv.Atoi(args.String["<timeout>"])
	if err != nil {
		return fmt.Errorf("error parsing timeout %q: %s", args.String["<timeout>"], err)
	}
	return client.UpdateApp(&ct.App{
		ID:            mustApp(),
		DeployTimeout: int32(timeout),
	})
}

func runGetDeployBatchSize(args *docopt.Args, client controller.Client) error {
	app, err := client.GetApp(mustApp())
	if err != nil {
		return err
	}
	batchSize := app.DeployBatchSize()
	if batchSize == nil {
		fmt.Println("not set")
	} else {
		fmt.Println(*batchSize)
	}
	return nil
}

func runSetDeployBatchSize(args *docopt.Args, client controller.Client) error {
	batchSize, err := strconv.Atoi(args.String["<size>"])
	if err != nil {
		return fmt.Errorf("error parsing batch-size %q: %s", args.String["<size>"], err)
	}
	app := &ct.App{ID: mustApp()}
	app.SetDeployBatchSize(batchSize)
	return client.UpdateApp(app)
}
