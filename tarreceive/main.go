package main

import (
	"bytes"
	"context"
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
	"regexp"
	"runtime"
	"strings"

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
	// fetchLayer reads the layer config stored for app (EDGE-35; "" is the
	// shared store); ct.ErrNotFound when absent.
	fetchLayer func(app, id string) (*ct.ImageLayer, error)
	// layerExists reports whether a layer squashfs is stored at url.
	layerExists func(url string) (bool, error)
	// createArtifact registers an artifact with the controller.
	createArtifact func(*ct.Artifact) error
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
		layerExists:  blobstoreHas,
		maxLayerSize: defaultMaxLayerSize,
		sem:          make(chan struct{}, defaultLayerConcurrency),
		squash:       mksquashfs,
		upload:       upload,
	}
	if client != nil {
		s.createArtifact = client.CreateArtifact
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
	app, code, msg := requestApp(r, tok)
	if code != 0 {
		httphelper.JSON(w, code, httphelper.JSONError{Code: errorCodeFor(code), Message: msg})
		return
	}
	s.router.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), appKey{}, app)))
}

type appKey struct{}

// appOf is the app a request's layers and manifest are stored for.
func appOf(r *http.Request) string {
	app, _ := r.Context().Value(appKey{}).(string)
	return app
}

var appIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]*$`)

// requestApp works out whose blobstore prefix a request stores layers and
// manifests under (EDGE-35), from the app query parameter and the token: a
// build token stores only under an app it has a grant for (the only one it
// has when it names none), so every layer it pushes counts toward that app's
// APP_QUOTA. The cluster key may name any app; naming none keeps the legacy
// shared /tarreceive/layers store that no quota counts (older CLIs and
// operator imports). A non-zero code refuses the request.
func requestApp(r *http.Request, tok *authorizer.Token) (string, int, string) {
	app := r.URL.Query().Get("app")
	if app != "" && !appIDPattern.MatchString(app) {
		return "", http.StatusBadRequest, fmt.Sprintf("invalid app %q", app)
	}
	if tok.HasClusterAdmin() {
		return app, 0, ""
	}
	grants := make(map[string]struct{}, len(tok.AppGrants))
	for _, g := range tok.AppGrants {
		if g.AppID != "" {
			grants[g.AppID] = struct{}{}
		}
	}
	if app == "" {
		if len(grants) != 1 {
			return "", http.StatusBadRequest, "this token has grants for several apps; name the app with ?app=<app id>"
		}
		for id := range grants {
			app = id
		}
		return app, 0, ""
	}
	if _, ok := grants[app]; !ok {
		return "", http.StatusForbidden, fmt.Sprintf("this token may not push layers for app %s", app)
	}
	return app, 0, ""
}

func errorCodeFor(status int) httphelper.ErrorCode {
	switch status {
	case http.StatusBadRequest:
		return httphelper.ValidationErrorCode
	case http.StatusForbidden:
		return httphelper.ForbiddenErrorCode
	case http.StatusRequestEntityTooLarge:
		return httphelper.RequestBodyTooBigErrorCode
	case http.StatusInsufficientStorage:
		return httphelper.InsufficientStorageErrorCode
	}
	return httphelper.UnknownErrorCode
}

func (s *server) handleGetLayer(w http.ResponseWriter, r *http.Request, p httprouter.Params) {
	layer, err := s.storedLayer(appOf(r), p.ByName("id"))
	if err == ct.ErrNotFound {
		http.NotFound(w, r)
		return
	} else if err != nil {
		s.uploadError(w, err)
		return
	}
	httphelper.JSON(w, http.StatusOK, layer)
}

// storedLayer is the layer id already stored for app, ready to reuse.
// Reusing a layer re-puts its config, which restarts the layer GC's grace
// period (flynn-blobstore cleanup-layers), and then checks that the squashfs
// is still there: a GC run that removed it first makes the layer missing, so
// the client uploads it again instead of naming a deleted object.
func (s *server) storedLayer(app, id string) (*ct.ImageLayer, error) {
	layer, err := s.fetchLayer(app, id)
	if err != nil {
		return nil, err
	}
	data, err := json.Marshal(layer)
	if err != nil {
		return nil, err
	}
	if err := s.upload(bytes.NewReader(data), utils.ConfigURLFor(app, id)); err != nil {
		return nil, err
	}
	ok, err := s.layerExists(utils.LayerURLFor(app, id))
	if err != nil {
		return nil, err
	}
	if !ok {
		log.Printf("layer %s (app %q) has a config but no squashfs, storing it again", id, app)
		return nil, ct.ErrNotFound
	}
	return layer, nil
}

// blobstoreHas reports whether the blobstore holds an object at url.
func blobstoreHas(url string) (bool, error) {
	req, err := http.NewRequest("HEAD", url, nil)
	if err != nil {
		return false, err
	}
	blobstoreauth.Apply(req)
	res, err := httphelper.RetryClient.Do(req)
	if err != nil {
		return false, err
	}
	res.Body.Close()
	switch res.StatusCode {
	case http.StatusOK:
		return true, nil
	case http.StatusNotFound:
		return false, nil
	}
	return false, fmt.Errorf("unexpected blobstore response to HEAD %s: %s", url, res.Status)
}

// blobstoreLayer reads the layer config tarreceive stored for app and id.
func blobstoreLayer(app, id string) (*ct.ImageLayer, error) {
	req, err := http.NewRequest("GET", utils.ConfigURLFor(app, id), nil)
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
	// Only this app's copy is reused (EDGE-35): a layer another app stored
	// is stored again here and counts toward this app's quota.
	app := appOf(r)
	if existing, err := s.storedLayer(app, id); err == nil {
		// Read the upload so the client sees the answer, not a reset.
		io.Copy(ioutil.Discard, body)
		httphelper.JSON(w, http.StatusOK, existing)
		return
	} else if err != ct.ErrNotFound {
		s.uploadError(w, err)
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
		if err := s.upload(layerFile, utils.LayerURLFor(app, id)); err != nil {
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
		if err := s.upload(bytes.NewReader(layerData), utils.ConfigURLFor(app, id)); err != nil {
			return nil, err
		}

		return layer, nil
	}()
	if body.Exceeded {
		s.layerTooLarge(w)
		return
	}
	if err != nil {
		s.uploadError(w, err)
		return
	}
	httphelper.JSON(w, http.StatusOK, layer)
}

// uploadError answers a failed request. A blobstore refusal because of a
// limit (413 MAX_OBJECT_SIZE, 507 APP_QUOTA) is passed on with the
// blobstore's message, which names the setting.
func (s *server) uploadError(w http.ResponseWriter, err error) {
	var be *blobstoreError
	if errors.As(err, &be) && (be.Status == http.StatusRequestEntityTooLarge || be.Status == http.StatusInsufficientStorage) {
		log.Printf("blobstore refused a layer upload: %s", be.Message)
		httphelper.JSON(w, be.Status, httphelper.JSONError{
			Code:    errorCodeFor(be.Status),
			Message: "blobstore refused the upload: " + be.Message,
		})
		return
	}
	httphelper.Error(w, err)
}

func (s *server) handleCreateArtifact(w http.ResponseWriter, r *http.Request, p httprouter.Params) {
	var image ct.ImageManifest
	if err := json.NewDecoder(r.Body).Decode(&image); err != nil {
		httphelper.Error(w, err)
		return
	}
	rawManifest := image.RawManifest()
	// the manifest and the layer template follow the app the layers were
	// stored for (EDGE-35); no app is the legacy shared store
	app := appOf(r)
	imageURL := blobstoreauth.TarreceiveImageURL(app, image.ID())
	if err := s.upload(bytes.NewReader(rawManifest), imageURL); err != nil {
		s.uploadError(w, err)
		return
	}
	artifact := &ct.Artifact{
		Type:             ct.ArtifactTypeFlynn,
		URI:              imageURL,
		Meta:             map[string]string{"blobstore": "true"},
		RawManifest:      rawManifest,
		Hashes:           image.Hashes(),
		Size:             int64(len(rawManifest)),
		LayerURLTemplate: utils.LayerURLTemplateFor(app),
	}
	if err := s.createArtifact(artifact); err != nil {
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
	if f, ok := data.(*os.File); ok {
		// send the length so the blobstore refuses a layer over
		// MAX_OBJECT_SIZE or APP_QUOTA before it is stored
		if info, err := f.Stat(); err == nil {
			if pos, err := f.Seek(0, io.SeekCurrent); err == nil && pos <= info.Size() {
				req.ContentLength = info.Size() - pos
			}
		}
	}
	blobstoreauth.Apply(req)
	res, err := httphelper.RetryClient.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		msg, _ := ioutil.ReadAll(io.LimitReader(res.Body, 4096))
		return &blobstoreError{Status: res.StatusCode, Message: strings.TrimSpace(string(msg))}
	}
	return nil
}

// blobstoreError is a blobstore answer other than 200 to a PUT.
type blobstoreError struct {
	Status  int
	Message string
}

func (e *blobstoreError) Error() string {
	return fmt.Sprintf("unexpected HTTP status: %d %s: %s", e.Status, http.StatusText(e.Status), e.Message)
}
