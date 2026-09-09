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
	if token := readSecret("/run/secrets/controller_token"); token != "" {
		client, err := controller.NewClientWithToken("", token)
		if err != nil {
			return err
		}
		tarClient := tarclient.NewClientWithToken("http://tarreceive.discoverd", token)
		if err := importImage(tarPath, client, tarClient); err != nil {
			if fallback, fbErr := legacyBuildClients(); fbErr == nil {
				return importImage(tarPath, fallback.controller, fallback.tar)
			}
			return err
		}
		return nil
	}
	clients, err := legacyBuildClients()
	if err != nil {
		return err
	}
	return importImage(tarPath, clients.controller, clients.tar)
}

type buildClients struct {
	controller controller.Client
	tar        *tarclient.Client
}

func legacyBuildClients() (*buildClients, error) {
	controllerKey := os.Getenv("CONTROLLER_KEY")
	if controllerKey == "" {
		controllerKey = readSecret("/run/secrets/controller_key")
	}
	if controllerKey == "" {
		return nil, fmt.Errorf("missing build credential")
	}
	client, err := controller.NewClient("", controllerKey)
	if err != nil {
		return nil, err
	}
	return &buildClients{
		controller: client,
		tar:        tarclient.NewClient("http://tarreceive.discoverd", controllerKey),
	}, nil
}

// readSecret returns the trimmed contents of a secret file, or "" if it is
// missing or unreadable.
func readSecret(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

func importImage(tarPath string, client controller.Client, tarClient *tarclient.Client) error {
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
