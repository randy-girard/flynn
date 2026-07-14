package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/cheggaaa/pb"
	cfg "github.com/flynn/flynn/cli/config"
	controller "github.com/flynn/flynn/controller/client"
	ct "github.com/flynn/flynn/controller/types"
	"github.com/flynn/flynn/pkg/backup"
	"github.com/flynn/flynn/pkg/dockerimage"
	"github.com/flynn/flynn/pkg/term"
	"github.com/flynn/flynn/pkg/version"
	"github.com/flynn/go-docopt"
)

func init() {
	register("docker", runDocker, `
usage: flynn docker push <image>
       flynn docker set-push-url [<url>]
       flynn docker login
       flynn docker logout


Deploy Docker images to a Flynn cluster.

Commands:
	push          push and release a Docker image to the cluster

	set-push-url  [DEPRECATED] set the Docker push URL (defaults to https://docker.$CLUSTER_DOMAIN)

	login         [DEPRECATED] run "docker login" against the cluster's docker-receive app

	logout        [DEPRECATED] run "docker logout" against the cluster's docker-receive app

Example:

	Assuming you have a Docker image tagged "my-custom-image:v2":

	$ flynn docker push my-custom-image:v2
	deploying Docker image: my-custom-image:v2
	exporting image with 'docker save my-custom-image:v2'
	111.58 MB 109.70 MB/s 1s
	uploading layer fccbfa2912f0cd6b9d13f91f288f112a2b825f3f758a4443aacb45bfc108cc74
	111.52 MB 25.56 MB/s 4s
	uploading layer e1a9a6284d0d24d8194ac84b372619e75cd35a46866b74925b7274c7056561e4
	15.50 KB 620.05 KB/s 0s
	uploading layer ac7299292f8b2f710d3b911c6a4e02ae8f06792e39822e097f9c4e9c2672b32d
	14.50 KB 601.45 KB/s 0s
	uploading layer a5e66470b2812e91798db36eb103c1f1e135bbe167e4b2ad5ba425b8db98ee8d
	5.50 KB 279.83 KB/s 0s
	uploading layer a8de0e025d94b33db3542e1e8ce58829144b30c6cd1fff057eec55b1491933c3
	3.00 KB 153.83 KB/s 0s
	Docker image deployed, scale it with 'flynn scale app=N'
`)
}

// minDockerPushTarVersion is the minimum API version which supports pushing
// Docker images as tar layers.
const minDockerPushTarVersion = "v20190425.0"

func runDocker(args *docopt.Args, client controller.Client) error {
	if args.Bool["set-push-url"] {
		return runDockerSetPushURL(args)
	} else if args.Bool["login"] {
		return runDockerLogin()
	} else if args.Bool["logout"] {
		return runDockerLogout()
	} else if args.Bool["push"] {
		return runDockerPush(args, client)
	}
	return errors.New("unknown docker subcommand")
}

func runDockerSetPushURL(args *docopt.Args) error {
	fmt.Fprintf(os.Stderr, "DEPRECATED: Pushing via a Docker registry has been deprecated in favour of pushing via the Flynn image service.\nIf the cluster is newer than %s then just run 'flynn docker push' directly.\n", minDockerPushTarVersion)
	cluster, err := getCluster()
	if err != nil {
		return err
	}
	url := args.String["<url>"]
	if url == "" {
		if cluster.DockerPushURL != "" {
			return fmt.Errorf("ERROR: refusing to overwrite current Docker push URL %q with a default one. To overwrite the existing URL, set one explicitly with 'flynn docker set-push-url URL'", cluster.DockerPushURL)
		}
		if !strings.Contains(cluster.ControllerURL, "controller") {
			return errors.New("ERROR: unable to determine default Docker push URL, set one explicitly with 'flynn docker set-push-url URL'")
		}
		url = strings.Replace(cluster.ControllerURL, "controller", "docker", 1)
	}
	if !strings.HasPrefix(url, "https://") {
		url = "https://" + url
	}
	cluster.DockerPushURL = url
	return config.SaveTo(configPath())
}

func runDockerLogin() error {
	fmt.Fprintf(os.Stderr, "DEPRECATED: Pushing via a Docker registry has been deprecated in favour of pushing via the Flynn image service.\nIf the cluster is newer than %s then just run 'flynn docker push' directly.\n", minDockerPushTarVersion)
	cluster, err := getCluster()
	if err != nil {
		return err
	}
	host, err := cluster.DockerPushHost()
	if err != nil {
		return err
	}
	err = dockerLogin(host, cluster.Key)
	if e, ok := err.(*exec.Error); ok && e.Err == exec.ErrNotFound {
		err = errors.New("Executable 'docker' was not found.")
	} else if err == ErrDockerTLSError {
		printDockerTLSWarning(host, cfg.CACertPath(cluster.Name))
		err = errors.New("Error configuring docker, follow the above instructions and try again.")
	}
	return err
}

func runDockerLogout() error {
	fmt.Fprintf(os.Stderr, "DEPRECATED: Pushing via a Docker registry has been deprecated in favour of pushing via the Flynn image service.\nIf the cluster is newer than %s then just run 'flynn docker push' directly.\n", minDockerPushTarVersion)
	cluster, err := getCluster()
	if err != nil {
		return err
	}
	host, err := cluster.DockerPushHost()
	if err != nil {
		return err
	}
	cmd := dockerLogoutCmd(host)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

var ErrDockerTLSError = errors.New("docker TLS error")

func dockerLogin(host, key string) error {
	return dockerLoginWithEmail(host, key, false)
}

func dockerLoginWithEmail(host, key string, useEmail bool) error {
	flags := []string{"--username=user", "--password=" + key}
	if useEmail {
		flags = append(flags, "--email=user@"+host)
	}
	cmd := exec.Command("docker", append([]string{"login"}, append(flags, host)...)...)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	err := cmd.Run()
	switch {
	case !useEmail && strings.Contains(out.String(), "Email: EOF"):
		return dockerLoginWithEmail(host, key, true)
	case strings.Contains(out.String(), "certificate signed by unknown authority"):
		return ErrDockerTLSError
	case err != nil:
		return fmt.Errorf("error running `docker login`: %s - output: %q", err, out)
	}
	return nil
}

func dockerLogout(host string) error {
	return dockerLogoutCmd(host).Run()
}

func dockerLogoutCmd(host string) *exec.Cmd {
	return exec.Command("docker", "logout", host)
}

func printDockerTLSWarning(host, caPath string) {
	fmt.Printf(`
WARN: docker configuration failed with a TLS error.
WARN:
WARN: Copy the TLS CA certificate %s
WARN: to /etc/docker/certs.d/%s/ca.crt
WARN: on the docker daemon's host and restart docker.
WARN:
WARN: If using Docker for Mac, go to Docker -> Preferences
WARN: -> Advanced, add %q as an
WARN: Insecure Registry and hit "Apply & Restart".

`[1:], caPath, host, host)
}

func runDockerPush(args *docopt.Args, client controller.Client) error {
	status, err := client.Status()
	if err != nil {
		return err
	}
	v := version.Parse(status.Version)
	if !v.Dev && v.Before(version.Parse(minDockerPushTarVersion)) {
		fmt.Fprintf(os.Stderr, "DEPRECATED: Pushing via a Docker registry has been deprecated in favour of pushing via the Flynn image service.\nConsider upgrading your cluster to a version newer than %s.\n", minDockerPushTarVersion)
		return runDockerPushLegacy(args, client)
	}
	return runDockerPushTar(args, client)
}

func runDockerPushLegacy(args *docopt.Args, client controller.Client) error {
	cluster, err := getCluster()
	if err != nil {
		return err
	}
	dockerHost, err := cluster.DockerPushHost()
	if err != nil {
		return err
	}

	image := args.String["<image>"]

	app, err := client.GetApp(mustApp())
	if err != nil {
		return err
	}
	prevRelease, err := client.GetAppRelease(app.ID)
	if err == controller.ErrNotFound {
		prevRelease = &ct.Release{}
	} else if err != nil {
		return fmt.Errorf("error getting current app release: %s", err)
	}

	// get the image config to determine Cmd, Entrypoint and Env
	cmd := exec.Command("docker", "inspect", "-f", "{{ json .Config }}", image)
	log.Printf("flynn: getting image config with %q", strings.Join(cmd.Args, " "))
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return err
	}
	var config struct {
		Cmd          []string               `json:"Cmd"`
		Entrypoint   []string               `json:"Entrypoint"`
		Env          []string               `json:"Env"`
		ExposedPorts map[string]interface{} `json:"ExposedPorts"`
	}
	if err := json.NewDecoder(stdout).Decode(&config); err != nil {
		return err
	}
	if err := cmd.Wait(); err != nil {
		return err
	}

	// tag the docker image ready to be pushed
	tag := fmt.Sprintf("%s/%s:latest", dockerHost, app.Name)
	cmd = exec.Command("docker", "tag", image, tag)
	log.Printf("flynn: tagging Docker image with %q", strings.Join(cmd.Args, " "))
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return err
	}

	artifact, err := dockerPush(client, app.Name, tag)
	if err != nil {
		return err
	}

	// create and deploy a release with the image config and created artifact
	log.Printf("flynn: deploying release using artifact URI %s", artifact.URI)
	release := dockerimage.NewAppRelease(app.Name, prevRelease, artifact.ID, dockerimage.BuildResultFromInspect(
		config.Entrypoint, config.Cmd, config.Env, config.ExposedPorts,
	), dockerimage.ReleaseOptions{
		Env:  prevRelease.Env,
		Meta: prevRelease.Meta,
	})
	if release.Meta == nil {
		release.Meta = make(map[string]string, 1)
	}
	release.Meta["docker-receive"] = "true"

	if err := client.CreateRelease(app.ID, release); err != nil {
		return err
	}
	if err := client.DeployAppRelease(app.ID, release.ID, nil); err != nil {
		return err
	}
	log.Printf("flynn: image deployed, scale it with 'flynn scale app=N'")
	return nil
}

func dockerPush(client controller.Client, repo, tag string) (*ct.Artifact, error) {
	// subscribe to artifact events
	events := make(chan *ct.Event)
	stream, err := client.StreamEvents(ct.StreamEventsOptions{
		ObjectTypes: []ct.EventType{ct.EventTypeArtifact},
	}, events)
	if err != nil {
		return nil, err
	}
	defer stream.Close()

	// push the Docker image to docker-receive
	cmd := exec.Command("docker", "push", tag)
	log.Printf("flynn: pushing Docker image with %q", strings.Join(cmd.Args, " "))
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return nil, err
	}

	// wait for an artifact to be created
	log.Printf("flynn: image pushed, waiting for artifact creation")
	for {
		select {
		case event, ok := <-events:
			if !ok {
				return nil, fmt.Errorf("event stream closed unexpectedly: %s", stream.Err())
			}
			var artifact ct.Artifact
			if err := json.Unmarshal(event.Data, &artifact); err != nil {
				return nil, err
			}
			if artifact.Meta["docker-receive.repository"] == repo {
				return &artifact, nil
			}
		case <-time.After(30 * time.Second):
			return nil, fmt.Errorf("timed out waiting for artifact creation")
		}
	}

}

func dockerSave(tag string, tw *backup.TarWriter, progress backup.ProgressBar) error {
	tmp, err := ioutil.TempFile("", "flynn-docker-save")
	if err != nil {
		return fmt.Errorf("error creating temp file: %s", err)
	}
	defer tmp.Close()
	defer os.Remove(tmp.Name())

	cmd := exec.Command("docker", "save", tag)
	cmd.Stdout = tmp
	if progress != nil {
		cmd.Stdout = io.MultiWriter(tmp, progress)
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return err
	}

	length, err := tmp.Seek(0, io.SeekCurrent)
	if err != nil {
		return err
	}
	if err := tw.WriteHeader("docker-image.tar", int(length)); err != nil {
		return err
	}
	if _, err := tmp.Seek(0, io.SeekStart); err != nil {
		return err
	}
	_, err = io.Copy(tw, tmp)
	return err
}

func runDockerPushTar(args *docopt.Args, client controller.Client) error {
	tag := args.String["<image>"]
	log.Printf("deploying Docker image: %s", tag)

	tarClient, err := clusterConf.TarClient()
	if err != nil {
		return err
	}

	app, err := client.GetApp(mustApp())
	if err != nil {
		return err
	}
	prevRelease, err := client.GetAppRelease(app.ID)
	if err == controller.ErrNotFound {
		prevRelease = &ct.Release{}
	} else if err != nil {
		return fmt.Errorf("error getting current app release: %s", err)
	}

	log.Printf("exporting image with 'docker save %s'", tag)
	cmd := exec.Command("docker", "save", tag)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return err
	}
	tmpDir, err := ioutil.TempDir("", "flynn-docker-push")
	if err != nil {
		return fmt.Errorf("error creating temporary directory: %s", err)
	}
	defer os.RemoveAll(tmpDir)
	if err := func() error {
		var src io.Reader = stdout
		if term.IsTerminal(os.Stderr.Fd()) {
			bar := pb.New(0)
			bar.SetUnits(pb.U_BYTES)
			bar.ShowBar = true
			bar.ShowSpeed = true
			bar.Output = os.Stderr
			bar.Start()
			defer bar.Finish()
			src = io.TeeReader(src, bar)
		}
		return dockerimage.UnpackSave(src, tmpDir)
	}(); err != nil {
		return fmt.Errorf("error extracting docker save output: %s", err)
	}
	if err := cmd.Wait(); err != nil {
		return err
	}

	artifact, build, err := dockerimage.PushFromSaveDir(tarClient, tmpDir)
	if err != nil {
		return err
	}

	release := dockerimage.NewAppRelease(app.Name, prevRelease, artifact.ID, build, dockerimage.ReleaseOptions{
		Env:  prevRelease.Env,
		Meta: prevRelease.Meta,
	})

	if err := client.CreateRelease(app.ID, release); err != nil {
		return err
	}
	if err := client.DeployAppRelease(app.ID, release.ID, nil); err != nil {
		return err
	}
	log.Printf("Docker image deployed, scale it with 'flynn scale app=N'")
	return nil
}
