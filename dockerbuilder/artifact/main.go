package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"strings"

	"github.com/docker/go-units"
	controller "github.com/flynn/flynn/controller/client"
	"github.com/flynn/flynn/pkg/dockerimage"
	tarclient "github.com/flynn/flynn/tarreceive/client"
)

func main() {
	tarPath := flag.String("tar", "", "path to docker save tarball")
	flag.Parse()

	if *tarPath == "" {
		fmt.Fprintln(os.Stderr, "missing --tar")
		os.Exit(1)
	}

	if err := run(*tarPath); err != nil {
		log.Fatalln("ERROR:", err)
	}
}

func run(tarPath string) error {
	controllerKey := os.Getenv("CONTROLLER_KEY")
	if controllerKey == "" {
		if data, err := os.ReadFile("/run/secrets/controller_key"); err == nil {
			controllerKey = strings.TrimSpace(string(data))
		}
	}
	if controllerKey == "" {
		return fmt.Errorf("missing CONTROLLER_KEY")
	}

	client, err := controller.NewClient("", controllerKey)
	if err != nil {
		return err
	}
	tarClient := tarclient.NewClient("http://tarreceive.discoverd", controllerKey)

	imageID := os.Getenv("IMAGE_ARTIFACT_ID")
	if imageID == "" {
		return fmt.Errorf("missing IMAGE_ARTIFACT_ID")
	}

	if _, err := dockerimage.ImportSaveFile(tarPath, dockerimage.ImportOptions{
		TarClient:        tarClient,
		ControllerClient: client,
		ArtifactID:       imageID,
	}); err != nil {
		return err
	}

	info, err := os.Stat(tarPath)
	if err != nil {
		return err
	}

	fmt.Printf("-----> Image size is %s\n", units.BytesSize(float64(info.Size())))
	return nil
}
