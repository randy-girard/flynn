package client

import (
	"crypto/tls"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"time"

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

	// retrySleep waits before a layer is sent again (time.Sleep).
	retrySleep func(time.Duration)
}

// maxBusyRetries is how many times a layer read from a file is sent again
// after a retryable 503 from tarreceive.
const maxBusyRetries = 10

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
	header := http.Header{}
	if flat {
		header.Set("X-Flynn-Flattened-Layer", "1")
	}
	size, sized := remaining(src)
	var start int64
	if sized {
		start, _ = src.(*os.File).Seek(0, io.SeekCurrent)
	}
	for attempt := 0; ; attempt++ {
		var layer ct.ImageLayer
		var body interface{} = src
		if sized {
			// send the length so tarreceive can refuse an oversize layer
			// before the upload (EDGE-08). The transport closes a body
			// that is a Closer; the file must stay open for a retry.
			body = httpclient.SizedBody{Reader: struct{ io.Reader }{src}, Size: size}
		}
		res, err := c.RawReq("POST", c.path("/layer/"+id), header, body, &layer)
		// EDGE-36: tarreceive is receiving MAX_CONCURRENT_UPLOADS layers or
		// its temp disk is reserved (a JSON 503), or the router gave up on
		// it while it waited for a slot without reading the body (a plain
		// 503). A layer is named by its hash, so a file can be sent again.
		if sized && attempt < maxBusyRetries && busy(res, err) {
			c.sleep(retryDelay(res))
			if _, serr := src.(*os.File).Seek(start, io.SeekStart); serr == nil {
				continue
			}
		}
		return &layer, err
	}
}

// busy reports whether the answer was a 503 (tarreceive's retryable one,
// or the router's).
func busy(res *http.Response, err error) bool {
	return err != nil && res != nil && res.StatusCode == http.StatusServiceUnavailable
}

// retryDelay is the Retry-After of res, 30 seconds when it has none, at
// most a minute.
func retryDelay(res *http.Response) time.Duration {
	d := 30 * time.Second
	if res != nil {
		if n, err := strconv.Atoi(res.Header.Get("Retry-After")); err == nil && n > 0 {
			d = time.Duration(n) * time.Second
		}
	}
	if d > time.Minute {
		d = time.Minute
	}
	return d
}

func (c *Client) sleep(d time.Duration) {
	if c.retrySleep != nil {
		c.retrySleep(d)
		return
	}
	time.Sleep(d)
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
