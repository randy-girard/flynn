package main

import (
	"errors"
	"log"

	"github.com/flynn/go-docopt"
	"github.com/randy-girard/flynn/controller/client"
	"github.com/randy-girard/flynn/pkg/cliutil"
)

func init() {
	register("ps:kill", runKill, `
usage: flynn ps:kill <job>...

Kill running jobs.`)
}

func runKill(args *docopt.Args, client controller.Client) error {
	success := true
	for _, job := range cliutil.List(args, "<job>") {
		if err := client.DeleteJob(mustApp(), job); err != nil {
			success = false
			log.Printf("ERROR: could not kill job %s: %s\n", job, err)
			continue
		}
		log.Printf("Job %s killed.", job)
	}
	if !success {
		return errors.New("Could not kill all jobs.")
	}
	return nil
}
