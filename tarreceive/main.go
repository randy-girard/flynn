package main

import (
	"bytes"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"

	"github.com/julienschmidt/httprouter"
	"github.com/randy-girard/flynn/controller/authorizer"
	"github.com/randy-girard/flynn/controller/authz"
	controller "github.com/randy-girard/flynn/controller/client"
	ct "github.com/randy-girard/flynn/controller/types"
	"github.com/randy-girard/flynn/pkg/archive"
	"github.com/randy-girard/flynn/pkg/blobstoreauth"
	"github.com/randy-girard/flynn/pkg/httphelper"
	"github.com/randy-girard/flynn/pkg/squashfs"
	"github.com/randy-girard/flynn/pkg/status"
	"github.com/randy-girard/flynn/pkg/uploadlimit"
	"github.com/randy-girard/flynn/tarreceive/utils"
)

func main() {
	controllerKey := os.Getenv("CONTROLLER_KEY")
	if controllerKey == "" {
		log.Fatal("missing CONTROLLER_KEY env var")
	}
	client, err := controller.NewClient("", controllerKey)
	if err != nil {
		log.Fatal(err)
	}

	authKey := os.Getenv("AUTH_KEY")
	if authKey == "" {
		log.Fatal("missing AUTH_KEY env var")
	}
	tokenKey, err := authorizer.ParseTokenKey(os.Getenv("ACCESS_TOKEN_KEY"))
	if err != nil {
		log.Fatalln("error decoding ACCESS_TOKEN_KEY:", err)
	}
	tokenMaxValidity, err := authorizer.ParseTokenMaxValidity(os.Getenv("ACCESS_TOKEN_MAX_VALIDITY"))
	if err != nil {
		log.Fatalln("error parsing ACCESS_TOKEN_MAX_VALIDITY:", err)
	}
	auth := authorizer.New([]string{authKey}, nil, tokenKey, tokenMaxValidity)

	srv := newServer(auth, client)
	if srv.maxLayerSize, err = uploadlimit.FromEnv("MAX_LAYER_SIZE", defaultMaxLayerSize); err != nil {
		log.Fatalln("error parsing MAX_LAYER_SIZE:", err)
	}
	concurrency, err := uploadlimit.FromEnv("MAX_CONCURRENT_LAYERS", defaultLayerConcurrency)
	if err != nil {
		log.Fatalln("error parsing MAX_CONCURRENT_LAYERS:", err)
	}
	if concurrency > 0 {
		srv.sem = make(chan struct{}, concurrency)
	} else {
		srv.sem = nil
	}
	log.Printf("layer limits: MAX_LAYER_SIZE=%d MAX_CONCURRENT_LAYERS=%d (0 is unlimited)", srv.maxLayerSize, concurrency)

	handler := httphelper.ContextInjector(
		"tarreceive",
		httphelper.NewRequestLogger(srv),
	)

	log.Fatal(http.ListenAndServe(":"+os.Getenv("PORT"), handler))
}

// Defaults for the layer limits (EDGE-08). flynn docker:push sends the whole
// image as one flattened, uncompressed tar, so the size limit has to fit the
// largest image an operator deploys; 32 GiB covers large CUDA/ML images.
// Operators change them with `flynn -a tarreceive env:set`.
const (
	defaultMaxLayerSize     = 32 << 30
	defaultLayerConcurrency = 2
)

type server struct {
	router *httprouter.Router
	auth   *authorizer.Authorizer
	client controller.Client
	// fetchLayer reads a stored layer config; ct.ErrNotFound when absent.
	fetchLayer func(id string) (*ct.ImageLayer, error)
	// maxLayerSize caps the layer tar a POST may send; 0 is no limit.
	maxLayerSize int64
	// sem bounds how many layers unpack and run mksquashfs at once; nil
	// is no limit.
	sem chan struct{}
	// squash builds the squashfs at dst from the directory src.
	squash func(src, dst string) error
	// upload PUTs data to a blobstore URL.
	upload func(data io.Reader, url string) error
}

func newServer(auth *authorizer.Authorizer, client controller.Client) *server {
	s := &server{
		router:       httprouter.New(),
		auth:         auth,
		client:       client,
		fetchLayer:   blobstoreLayer,
		maxLayerSize: defaultMaxLayerSize,
		sem:          make(chan struct{}, defaultLayerConcurrency),
		squash:       mksquashfs,
		upload:       upload,
	}
	s.router.GET("/layer/:id", s.handleGetLayer)
	s.router.POST("/layer/:id", s.handleCreateLayer)
	s.router.POST("/artifact", s.handleCreateArtifact)
	return s
}

func (s *server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == status.Path {
		status.HealthyHandler.ServeHTTP(w, r)
		return
	}
	tok, err := s.auth.AuthorizeRequest(r)
	if err != nil {
		w.WriteHeader(401)
		return
	}
	// tarreceive proxies layer/artifact uploads to the controller/blobstore
	// using its own cluster key, so a non-admin caller must be an authorized
	// builder: it must carry the build:artifacts scope plus an app grant.
	// Otherwise a leaked app-scoped token could push arbitrary image layers.
	if !authz.TarreceiveAllowed(tok) {
		w.WriteHeader(403)
		return
	}
	s.router.ServeHTTP(w, r)
}

func (s *server) handleGetLayer(w http.ResponseWriter, r *http.Request, p httprouter.Params) {
	layer, err := s.fetchLayer(p.ByName("id"))
	if err == ct.ErrNotFound {
		http.NotFound(w, r)
		return
	} else if err != nil {
		httphelper.Error(w, err)
		return
	}
	httphelper.JSON(w, http.StatusOK, layer)
}

// blobstoreLayer reads the layer config tarreceive stored for id.
func blobstoreLayer(id string) (*ct.ImageLayer, error) {
	req, err := http.NewRequest("GET", utils.ConfigURL(id), nil)
	if err != nil {
		return nil, err
	}
	blobstoreauth.Apply(req)
	res, err := httphelper.RetryClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode == http.StatusNotFound {
		return nil, ct.ErrNotFound
	} else if res.StatusCode != http.StatusOK {
		body, _ := ioutil.ReadAll(res.Body)
		return nil, fmt.Errorf("unexpected blobstore response: %s: %s", res.Status, body)
	}
	var layer ct.ImageLayer
	if err := json.NewDecoder(res.Body).Decode(&layer); err != nil {
		return nil, err
	}
	return &layer, nil
}

// mksquashfs builds a layer squashfs with the shared layer arguments.
func mksquashfs(src, dst string) error {
	if out, err := exec.Command("mksquashfs", squashfs.LayerArgs(src, dst, "-processors", squashfs.ProcessorCount(runtime.NumCPU()))...).CombinedOutput(); err != nil {
		return fmt.Errorf("mksquashfs error: %s: %s", err, out)
	}
	return nil
}

// layerTooLarge answers 413 naming the limit and the setting that raises it.
func (s *server) layerTooLarge(w http.ResponseWriter) {
	httphelper.JSON(w, http.StatusRequestEntityTooLarge, httphelper.JSONError{
		Code: httphelper.RequestBodyTooBigErrorCode,
		Message: fmt.Sprintf("layer exceeds the tarreceive limit of %s; an operator can raise it with `flynn -a tarreceive env:set MAX_LAYER_SIZE=<size>`",
			uploadlimit.Format(s.maxLayerSize)),
	})
}

func (s *server) handleCreateLayer(w http.ResponseWriter, r *http.Request, p httprouter.Params) {
	id := p.ByName("id")
	flat := r.Header.Get("X-Flynn-Flattened-Layer") == "1"

	// EDGE-08: refuse a body over the limit before writing any of it.
	if s.maxLayerSize > 0 && r.ContentLength > s.maxLayerSize {
		s.layerTooLarge(w)
		return
	}
	body := uploadlimit.NewReader(r.Body, s.maxLayerSize)

	// The layer is named by the SHA256 of its tar. Once its config is
	// stored, keep it: rebuilding can produce different squashfs bytes, and
	// images that already name the old hash would no longer verify (NEW-05).
	if existing, err := s.fetchLayer(id); err == nil {
		// Read the upload so the client sees the answer, not a reset.
		io.Copy(ioutil.Discard, body)
		httphelper.JSON(w, http.StatusOK, existing)
		return
	} else if err != ct.ErrNotFound {
		httphelper.Error(w, err)
		return
	}

	layer, err := func() (*ct.ImageLayer, error) {
		// copy the tar to a temp file and generate its SHA256 hash
		tmpDir, err := ioutil.TempDir("", "")
		if err != nil {
			return nil, err
		}
		defer os.RemoveAll(tmpDir)
		tarPath := filepath.Join(tmpDir, "layer.tar")
		tarFile, err := os.Create(tarPath)
		if err != nil {
			return nil, err
		}
		tarHash := sha256.New()
		if _, err := io.Copy(io.MultiWriter(tarFile, tarHash), body); err != nil {
			tarFile.Close()
			return nil, err
		}
		if err := tarFile.Close(); err != nil {
			return nil, err
		}

		// check the SHA256 hash
		if hex.EncodeToString(tarHash.Sum(nil)) != id {
			return nil, errors.New("SHA256 mismatch")
		}

		// unpack and squash hold a slot: both use the disk and every CPU
		if s.sem != nil {
			select {
			case s.sem <- struct{}{}:
				defer func() { <-s.sem }()
			case <-r.Context().Done():
				return nil, r.Context().Err()
			}
		}

		// extract the tar
		tarFile, err = os.Open(tarPath)
		if err != nil {
			return nil, err
		}
		extractDir := filepath.Join(tmpDir, "extract")
		if err := os.MkdirAll(extractDir, 0755); err != nil {
			tarFile.Close()
			return nil, err
		}
		if flat {
			err = archive.UnpackFlat(tarFile, extractDir, true)
		} else {
			err = archive.Unpack(tarFile, extractDir, true)
		}
		tarFile.Close()
		if err != nil {
			return nil, err
		}
		if err := os.Remove(tarPath); err != nil {
			return nil, err
		}

		// create squashfs layer
		layerPath := filepath.Join(tmpDir, "layer.squashfs")
		if err := s.squash(extractDir, layerPath); err != nil {
			return nil, err
		}

		// generate squashfs layer SHA
		layerFile, err := os.Open(layerPath)
		if err != nil {
			return nil, err
		}
		defer layerFile.Close()
		layerHash := sha512.New512_256()
		layerSize, err := io.Copy(layerHash, layerFile)
		if err != nil {
			return nil, err
		}
		layerSha := hex.EncodeToString(layerHash.Sum(nil))

		// upload squashfs layer
		if _, err := layerFile.Seek(0, os.SEEK_SET); err != nil {
			return nil, err
		}
		if err := s.upload(layerFile, utils.LayerURL(id)); err != nil {
			return nil, err
		}

		// upload layer JSON
		layer := &ct.ImageLayer{
			ID:     id,
			Type:   ct.ImageLayerTypeSquashfs,
			Length: layerSize,
			Hashes: map[string]string{"sha512_256": layerSha},
			Meta:   map[string]string{"tar.layer_id": id},
		}
		layerData, err := json.Marshal(layer)
		if err != nil {
			return nil, err
		}
		if err := s.upload(bytes.NewReader(layerData), utils.ConfigURL(id)); err != nil {
			return nil, err
		}

		return layer, nil
	}()
	if body.Exceeded {
		s.layerTooLarge(w)
		return
	}
	if err != nil {
		httphelper.Error(w, err)
		return
	}
	httphelper.JSON(w, http.StatusOK, layer)
}

func (s *server) handleCreateArtifact(w http.ResponseWriter, r *http.Request, p httprouter.Params) {
	var image ct.ImageManifest
	if err := json.NewDecoder(r.Body).Decode(&image); err != nil {
		httphelper.Error(w, err)
		return
	}
	rawManifest := image.RawManifest()
	imageURL := fmt.Sprintf("http://blobstore.discoverd/tarreceive/images/%s.json", image.ID())
	if err := s.upload(bytes.NewReader(rawManifest), imageURL); err != nil {
		httphelper.Error(w, err)
		return
	}
	artifact := &ct.Artifact{
		Type:             ct.ArtifactTypeFlynn,
		URI:              imageURL,
		Meta:             map[string]string{"blobstore": "true"},
		RawManifest:      rawManifest,
		Hashes:           image.Hashes(),
		Size:             int64(len(rawManifest)),
		LayerURLTemplate: utils.LayerURLTemplate,
	}
	if err := s.client.CreateArtifact(artifact); err != nil {
		httphelper.Error(w, err)
		return
	}
	httphelper.JSON(w, http.StatusOK, artifact)
}

func upload(data io.Reader, url string) error {
	req, err := http.NewRequest("PUT", url, data)
	if err != nil {
		return err
	}
	blobstoreauth.Apply(req)
	res, err := httphelper.RetryClient.Do(req)
	if err != nil {
		return err
	}
	res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("unexpected HTTP status: %s", res.Status)
	}
	return nil
}
