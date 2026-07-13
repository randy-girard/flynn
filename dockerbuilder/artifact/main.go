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
	// Prefer a minted, app-scoped build token delivered via a root-only secret
	// mount; fall back to the legacy cluster-wide CONTROLLER_KEY for clusters
	// that have not been re-bootstrapped with a signing key.
	if token := readSecret("/run/secrets/controller_token"); token != "" {
		client, err := controller.NewClientWithToken("", token)
		if err != nil {
			return err
		}
		tarClient := tarclient.NewClientWithToken("http://tarreceive.discoverd", token)
		return importImage(tarPath, client, tarClient)
	}

	controllerKey := os.Getenv("CONTROLLER_KEY")
	if controllerKey == "" {
		controllerKey = readSecret("/run/secrets/controller_key")
	}
	if controllerKey == "" {
		return fmt.Errorf("missing build credential")
	}

	client, err := controller.NewClient("", controllerKey)
	if err != nil {
		return err
	}
	tarClient := tarclient.NewClient("http://tarreceive.discoverd", controllerKey)
	return importImage(tarPath, client, tarClient)
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
