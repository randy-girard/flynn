package client

import (
	"crypto/tls"
	"errors"
	"io"
	"net/http"

	ct "github.com/flynn/flynn/controller/types"
	"github.com/flynn/flynn/pkg/httpclient"
	"github.com/flynn/flynn/pkg/httphelper"
	"github.com/flynn/flynn/pkg/pinned"
)

var ErrNotFound = errors.New("layer not found")

type Config struct {
	Pin    []byte
	Domain string
}

type Client struct {
	*httpclient.Client
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
	return &layer, c.Get("/layer/"+id, &layer)
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
	return &layer, c.PostWithHeader("/layer/"+id, header, src, &layer)
}

func (c *Client) CreateArtifact(manifest *ct.ImageManifest) (*ct.Artifact, error) {
	var artifact ct.Artifact
	return &artifact, c.Post("/artifact", manifest, &artifact)
}
