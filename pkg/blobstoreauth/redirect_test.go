package blobstoreauth

import (
	"net/http"
	"testing"
)

// redirectClient is an http.Client on a Transport whose base answers by host:
// every host in redirects answers 302 to its target, any other host 200. It
// records the Authorization header each host received.
func redirectClient(redirects map[string]string) (*http.Client, map[string]string) {
	seen := make(map[string]string)
	base := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		seen[req.URL.Host] = req.Header.Get("Authorization")
		if loc, ok := redirects[req.URL.Host]; ok {
			h := make(http.Header)
			h.Set("Location", loc)
			return &http.Response{StatusCode: http.StatusFound, Header: h, Body: http.NoBody, Request: req}, nil
		}
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: http.NoBody, Request: req}, nil
	})
	return &http.Client{Transport: &Transport{Base: base}}, seen
}

func TestTransportDoesNotAddKeyOnRedirectIntoBlobstore(t *testing.T) {
	t.Setenv("AUTH_KEY", "secret")
	t.Setenv("DISCOVERD", "")
	client, seen := redirectClient(map[string]string{
		"evil.example": "http://blobstore.discoverd/slugs/apps/other/slug.tgz",
	})
	res, err := client.Get("http://evil.example/artifact")
	if err == nil {
		res.Body.Close()
	}
	if auth, reached := seen["blobstore.discoverd"]; reached && auth != "" {
		t.Fatalf("a redirect from a foreign host reached the blobstore with Authorization=%q", auth)
	}
	if seen["evil.example"] != "" {
		t.Fatalf("foreign host got Authorization=%q", seen["evil.example"])
	}
}

func TestTransportDoesNotForwardKeyOnRedirectOutOfBlobstore(t *testing.T) {
	t.Setenv("AUTH_KEY", "secret")
	t.Setenv("DISCOVERD", "")
	client, seen := redirectClient(map[string]string{
		"blobstore.discoverd": "http://s3.example/bucket/object?signature=x",
	})
	res, err := client.Get("http://blobstore.discoverd/layers/x.squashfs")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if seen["blobstore.discoverd"] == "" {
		t.Fatal("the blobstore request carried no key")
	}
	if auth := seen["s3.example"]; auth != "" {
		t.Fatalf("redirect target got Authorization=%q", auth)
	}
}

func TestTransportKeepsKeyOnRedirectWithinBlobstore(t *testing.T) {
	t.Setenv("AUTH_KEY", "secret")
	t.Setenv("DISCOVERD", "")
	client, seen := redirectClient(map[string]string{
		"blobstore.discoverd:80": "http://blobstore.discoverd/layers/y.squashfs",
	})
	res, err := client.Get("http://blobstore.discoverd:80/layers/x.squashfs")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if seen["blobstore.discoverd"] == "" {
		t.Fatal("a redirect within the blobstore lost the key")
	}
}
