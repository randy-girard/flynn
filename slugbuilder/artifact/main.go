package main

import (
	"bytes"
	"crypto/sha512"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/docker/go-units"
	controller "github.com/randy-girard/flynn/controller/client"
	ct "github.com/randy-girard/flynn/controller/types"
	"github.com/randy-girard/flynn/pkg/blobstoreauth"
	hh "github.com/randy-girard/flynn/pkg/httphelper"
	"github.com/randy-girard/flynn/pkg/squashfs"
	"github.com/randy-girard/flynn/pkg/typeconv"
	"gopkg.in/yaml.v2"
)

func main() {
	dir := flag.String("dir", "", "slug parent directory")
	uid := flag.Int("uid", -1, "UID")
	gid := flag.Int("gid", -1, "GID")
	flag.Parse()

	if *dir == "" {
		fmt.Fprintf(os.Stderr, "missing --dir")
		os.Exit(1)
	}
	if *uid < 0 {
		fmt.Fprintf(os.Stderr, "missing --uid")
		os.Exit(1)
	}
	if *gid < 0 {
		fmt.Fprintf(os.Stderr, "missing --gid")
		os.Exit(1)
	}

	if err := run(*dir, *uid, *gid); err != nil {
		log.Fatalln("ERROR:", "could not create slug image:", err)
	}
}

// newControllerClient builds a controller client for create-artifact. It
// prefers a minted, app-scoped build token delivered via a root-only secret
// mount (authenticated with a bearer token). A leftover CONTROLLER_KEY file
// at /run/secrets/controller_key is accepted only as defense in depth if
// gitreceive still relocated one. Gitreceive no longer injects CONTROLLER_KEY
// into build job env.
func newControllerClient() (client controller.Client, usedToken bool, err error) {
	if token := readSecret("/run/secrets/controller_token"); token != "" {
		c, err := controller.NewClientWithToken("", token)
		return c, true, err
	}
	c, err := legacyControllerClient()
	return c, false, err
}

func legacyControllerClient() (controller.Client, error) {
	controllerKey := os.Getenv("CONTROLLER_KEY")
	if controllerKey == "" {
		controllerKey = readSecret("/run/secrets/controller_key")
	}
	if controllerKey == "" {
		return nil, fmt.Errorf("missing build credential")
	}
	return controller.NewClient("", controllerKey)
}

// readSecret returns the trimmed contents of a secret file, or "" if it is
// missing or unreadable.
func readSecret(path string) string {
	data, err := ioutil.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

func run(dir string, uid, gid int) error {
	client, usedToken, err := newControllerClient()
	if err != nil {
		return err
	}

	// create a squashfs layer
	layer, err := ioutil.TempFile("", "squashfs-")
	if err != nil {
		return err
	}
	defer os.Remove(layer.Name())
	defer layer.Close()

	if out, err := exec.Command("mksquashfs", squashfs.Args(dir, layer.Name())...).CombinedOutput(); err != nil {
		return fmt.Errorf("mksquashfs error: %s: %s", err, out)
	}

	h := sha512.New512_256()
	length, err := io.Copy(h, layer)
	if err != nil {
		return err
	}
	layerSHA := hex.EncodeToString(h.Sum(nil))

	// upload the layer to the app's own blobstore prefix; the build token
	// reaches no other (NEW-05)
	appID := os.Getenv("FLYNN_APP_ID")
	fmt.Println("-----> Uploading slug to blobstore")
	layerURL := blobstoreauth.SlugLayerURL(appID, layerSHA)
	if err := uploadSlug(layer, layerURL); err != nil {
		return err
	}

	manifest := &ct.ImageManifest{
		Type: ct.ImageManifestTypeV1,

		Entrypoints: map[string]*ct.ImageEntrypoint{
			"_default": {
				Env: map[string]string{
					"PATH": "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
					"TERM": "xterm",
					"HOME": "/app",
				},
				WorkingDir: "/app",
				Args:       []string{"/runner/init", "bash"},
				Uid:        typeconv.Uint32Ptr(uint32(uid)),
				Gid:        typeconv.Uint32Ptr(uint32(gid)),
			},
		},

		Rootfs: []*ct.ImageRootfs{{
			Platform: ct.ImagePlatformFor(""),
			Layers: []*ct.ImageLayer{{
				ID:     layerSHA,
				Type:   ct.ImageLayerTypeSquashfs,
				Length: length,
				Hashes: map[string]string{"sha512_256": layerSHA},
			}},
		}},
	}

	artifact := slugArtifact(appID, os.Getenv("SLUG_IMAGE_ID"), manifest)
	if err := upload(bytes.NewReader(artifact.RawManifest), artifact.URI); err != nil {
		return err
	}

	processCommands := determineProcessCommands(dir)
	artifact.Meta["slugbuilder.process_types"] = strings.Join(processTypeNames(processCommands), ",")
	if len(processCommands) > 0 {
		if raw, err := json.Marshal(processCommands); err == nil {
			artifact.Meta["slugbuilder.process_commands"] = string(raw)
		}
	}

	// create artifact
	fallback := func() (artifactCreator, error) { return legacyControllerClient() }
	if err := createArtifact(client, usedToken, fallback, artifact); err != nil {
		return err
	}

	fmt.Printf("-----> Compiled slug size is %s\n", units.BytesSize(float64(length)))
	return nil
}

// slugArtifact is the artifact for a slug image stored under appID's
// blobstore prefix (the legacy shared one when appID is empty).
func slugArtifact(appID, id string, manifest *ct.ImageManifest) *ct.Artifact {
	rawManifest := manifest.RawManifest()
	return &ct.Artifact{
		ID:               id,
		Type:             ct.ArtifactTypeFlynn,
		URI:              blobstoreauth.SlugImageURL(appID, manifest.ID()),
		Meta:             map[string]string{"blobstore": "true"},
		RawManifest:      rawManifest,
		Hashes:           manifest.Hashes(),
		Size:             int64(len(rawManifest)),
		LayerURLTemplate: blobstoreauth.SlugLayerURLTemplate(appID),
	}
}

const maxSlugUploadAttempts = 3

func uploadSlug(slugFile *os.File, url string) (err error) {
	tryUpload := func() error {
		if _, err := slugFile.Seek(0, io.SeekStart); err != nil {
			return err
		}
		// use a NopCloser to ensure failed requests don't close
		// the slug file
		data := ioutil.NopCloser(slugFile)
		return upload(data, url)
	}
	for attempt := 1; attempt <= maxSlugUploadAttempts; attempt++ {
		err = tryUpload()
		if err == nil {
			return nil
		}
		fmt.Printf("WARN: error uploading to blobstore (attempt %d of %d): %s\n", attempt, maxSlugUploadAttempts, err)
	}
	return
}

func upload(data io.Reader, url string) error {
	req, err := http.NewRequest("PUT", url, data)
	if err != nil {
		return err
	}
	blobstoreauth.Apply(req)
	res, err := hh.RetryClient.Do(req)
	if err != nil {
		return err
	}
	res.Body.Close()
	// 409: the content-addressed object is already stored under this name,
	// which is the hash of these bytes. The blobstore refuses to replace it.
	if res.StatusCode != http.StatusOK && res.StatusCode != http.StatusConflict {
		return fmt.Errorf("unexpected HTTP status: %s", res.Status)
	}
	return nil
}

func determineProcessCommands(dir string) map[string]string {
	cmds := loadProcfileCommands(dir)
	if len(cmds) == 0 {
		cmds = loadDefaultCommands(dir)
	}
	return cmds
}

func processTypeNames(cmds map[string]string) []string {
	types := make([]string, 0, len(cmds))
	for typ := range cmds {
		typ = strings.TrimSpace(typ)
		if typ == "" {
			continue
		}
		types = append(types, typ)
	}
	sort.Strings(types)
	return types
}

func loadProcfileCommands(dir string) map[string]string {
	data, err := ioutil.ReadFile(filepath.Join(dir, "app", "Procfile"))
	if err != nil {
		if !os.IsNotExist(err) {
			fmt.Fprintln(os.Stderr, "WARN: error reading Procfile:", err)
		}
		return nil
	}
	var procfile map[string]string
	if err := yaml.Unmarshal(data, &procfile); err != nil {
		fmt.Fprintln(os.Stderr, "WARN: error parsing Procfile:", err)
		return nil
	}
	return trimProcessCommands(procfile)
}

func loadDefaultCommands(dir string) map[string]string {
	data, err := ioutil.ReadFile(filepath.Join(dir, "app", ".release"))
	if err != nil {
		if !os.IsNotExist(err) {
			fmt.Fprintln(os.Stderr, "WARN: error reading .release:", err)
		}
		return nil
	}
	var release struct {
		DefaultProcessTypes map[string]string `yaml:"default_process_types"`
	}
	if err := yaml.Unmarshal(data, &release); err != nil {
		fmt.Fprintln(os.Stderr, "WARN: error parsing .release:", err)
		return nil
	}
	return trimProcessCommands(release.DefaultProcessTypes)
}

func trimProcessCommands(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for typ, cmd := range in {
		typ = strings.TrimSpace(typ)
		if typ == "" {
			continue
		}
		out[typ] = strings.TrimSpace(cmd)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
