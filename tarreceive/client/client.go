package client

import (
	"crypto/tls"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"

	ct "github.com/randy-girard/flynn/controller/types"
	"github.com/randy-girard/flynn/pkg/httpclient"
	"github.com/randy-girard/flynn/pkg/httphelper"
	"github.com/randy-girard/flynn/pkg/pinned"
)

var ErrNotFound = errors.New("layer not found")

type Config struct {
	Pin    []byte
	Domain string
}

type Client struct {
	*httpclient.Client
	// App is the ID of the app the layers and image manifests are for.
	// tarreceive stores them under that app's blobstore prefix, where they
	// count toward its APP_QUOTA (EDGE-35). Empty sends no app: a build
	// token then uses its only app, the cluster key the shared store.
	App string
}

// path adds the app to a request path.
func (c *Client) path(p string) string {
	if c.App == "" {
		return p
	}
	return p + "?app=" + url.QueryEscape(c.App)
}

func NewClient(url, key string) *Client {
	return newClient(url, key, httphelper.RetryClient)
}

func NewClientWithHTTP(url string, client *http.Client) *Client {
	return newClient(url, "", client)
}

// NewClientWithToken creates a Client that authenticates with a signed
// AccessToken JWT (Authorization: Bearer) rather than the cluster-wide
// controller key, for use by app-scoped build jobs.
func NewClientWithToken(url, token string) *Client {
	c := newClient(url, "", httphelper.RetryClient)
	c.Token = token
	return c
}

func NewClientWithConfig(url, key string, config Config) *Client {
	if config.Pin == nil {
		return NewClient(url, key)
	}
	d := &pinned.Config{Pin: config.Pin}
	if config.Domain != "" {
		d.Config = &tls.Config{ServerName: config.Domain}
	}
	httpClient := &http.Client{Transport: &http.Transport{DialTLS: d.Dial}}
	c := newClient(url, key, httpClient)
	c.Host = config.Domain
	return c
}

func newClient(url, key string, httpClient *http.Client) *Client {
	return &Client{
		Client: &httpclient.Client{
			ErrNotFound: ErrNotFound,
			URL:         url,
			Key:         key,
			HTTP:        httpClient,
		},
	}
}

func (c *Client) GetLayer(id string) (*ct.ImageLayer, error) {
	var layer ct.ImageLayer
	return &layer, c.Get(c.path("/layer/"+id), &layer)
}

func (c *Client) CreateLayer(id string, src io.Reader) (*ct.ImageLayer, error) {
	return c.createLayer(id, src, false)
}

// CreateFlatLayer uploads a flattened Docker rootfs tar (as produced by
// flynn docker push) for conversion to squashfs on the server.
func (c *Client) CreateFlatLayer(id string, src io.Reader) (*ct.ImageLayer, error) {
	return c.createLayer(id, src, true)
}

func (c *Client) createLayer(id string, src io.Reader, flat bool) (*ct.ImageLayer, error) {
	var layer ct.ImageLayer
	header := http.Header{}
	if flat {
		header.Set("X-Flynn-Flattened-Layer", "1")
	}
	var body interface{} = src
	if size, ok := remaining(src); ok {
		// send the length so tarreceive can refuse an oversize layer
		// before the upload (EDGE-08)
		body = httpclient.SizedBody{Reader: src, Size: size}
	}
	return &layer, c.PostWithHeader(c.path("/layer/"+id), header, body, &layer)
}

// remaining is the number of bytes left to read in a regular file.
func remaining(src io.Reader) (int64, bool) {
	f, ok := src.(*os.File)
	if !ok {
		return 0, false
	}
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return 0, false
	}
	pos, err := f.Seek(0, io.SeekCurrent)
	if err != nil || pos > info.Size() {
		return 0, false
	}
	return info.Size() - pos, true
}

func (c *Client) CreateArtifact(manifest *ct.ImageManifest) (*ct.Artifact, error) {
	var artifact ct.Artifact
	return &artifact, c.Post(c.path("/artifact"), manifest, &artifact)
}
