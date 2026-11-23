package main

import (
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/randy-girard/flynn/blobstore/data"
	"github.com/randy-girard/flynn/pkg/blobstoreauth"
	"github.com/randy-girard/flynn/pkg/uploadlimit"
)

// Default PUT limits (EDGE-08). They are generous on purpose: one object can
// be a whole Docker image flattened into a squashfs, a slug, or a build
// cache. Operators lower or raise them with `flynn -a blobstore env:set
// MAX_OBJECT_SIZE=<size> APP_QUOTA=<size>`; 0 turns a limit off.
const (
	defaultMaxObjectSize = 32 << 30
	defaultAppQuota      = 100 << 30
)

// putLimits bounds what one PUT may store. Zero fields are no limit.
type putLimits struct {
	// maxObject caps the size of one object (an append counts the bytes
	// already stored).
	maxObject int64
	// appQuota caps the live bytes stored under one app's names: its
	// /slugs/apps/<app>/ and /tarreceive/apps/<app>/ objects, build caches
	// and repo tarball.
	appQuota int64
	// usage reports the live bytes under app's names, leaving out exclude
	// (the object being written).
	usage func(app, exclude string) (int64, error)
	// idleTimeout cuts off a PUT body that sends nothing for this long.
	idleTimeout time.Duration
}

func putLimitsFromEnv(repo *data.FileRepo) (putLimits, error) {
	maxObject, err := uploadlimit.FromEnv("MAX_OBJECT_SIZE", defaultMaxObjectSize)
	if err != nil {
		return putLimits{}, fmt.Errorf("error parsing MAX_OBJECT_SIZE: %s", err)
	}
	quota, err := uploadlimit.FromEnv("APP_QUOTA", defaultAppQuota)
	if err != nil {
		return putLimits{}, fmt.Errorf("error parsing APP_QUOTA: %s", err)
	}
	return putLimits{maxObject: maxObject, appQuota: quota, usage: repo.AppUsage}, nil
}

// putBudget is how many bytes a PUT body may carry, and the answer to give
// when it carries more.
type putBudget struct {
	// limited is false when no limit applies.
	limited bool
	// left is the most the body may carry; 0 or less means not one byte.
	left int64
	code int
	msg  string
}

// budget works out the bytes a PUT of name at offset may add.
func (l putLimits) budget(name string, offset int64) (putBudget, error) {
	var b putBudget
	if l.maxObject > 0 {
		b = putBudget{
			limited: true,
			left:    l.maxObject - offset,
			code:    http.StatusRequestEntityTooLarge,
			msg: fmt.Sprintf("object exceeds the blobstore limit of %s; an operator can raise it with `flynn -a blobstore env:set MAX_OBJECT_SIZE=<size>`",
				uploadlimit.Format(l.maxObject)),
		}
	}
	app, ok := appForPath(name)
	if l.appQuota <= 0 || !ok || l.usage == nil {
		return b, nil
	}
	used, err := l.usage(app, name)
	if err != nil {
		return b, err
	}
	if left := l.appQuota - used - offset; !b.limited || left < b.left {
		b = putBudget{
			limited: true,
			left:    left,
			code:    http.StatusInsufficientStorage,
			msg: fmt.Sprintf("app %s already stores %d bytes and the blobstore quota per app is %s; delete old releases or caches, or an operator can raise it with `flynn -a blobstore env:set APP_QUOTA=<size>`",
				app, used, uploadlimit.Format(l.appQuota)),
		}
	}
	return b, nil
}

// limitPut runs put with req's body bounded by the limits. It answers the
// request itself when the body is too large and reports whether it did.
func (l putLimits) limitPut(w http.ResponseWriter, req *http.Request, name string, offset int64, put func(body io.Reader) error) (handled bool, err error) {
	b, err := l.budget(name, offset)
	if err != nil {
		return false, err
	}
	if !b.limited {
		return false, put(req.Body)
	}
	if req.ContentLength > b.left || (b.left <= 0 && req.ContentLength != 0) {
		// refused before an upload starts or a byte is stored
		log.Printf("refusing PUT %s: %s", name, b.msg)
		http.Error(w, b.msg, b.code)
		return true, nil
	}
	body := uploadlimit.NewBudgetReader(req.Body, b.left)
	err = put(body)
	if body.Exceeded {
		// the upload was discarded; an overwritten object is intact
		log.Printf("refusing PUT %s: %s", name, b.msg)
		http.Error(w, b.msg, b.code)
		return true, nil
	}
	return false, err
}

// appForPath returns the app whose storage a blob name counts against:
// /slugs/apps/<app>/..., /tarreceive/apps/<app>/..., the signed build cache
// names (/<app>-cache.tgz, /<app>-docker-cache.tgz) and /repos/<app>.tar.
func appForPath(p string) (string, bool) {
	if id, ok := blobstoreauth.CacheAppID(p); ok {
		return id, true
	}
	parts := strings.Split(strings.TrimPrefix(p, "/"), "/")
	switch {
	case len(parts) >= 4 && (parts[0] == "slugs" || parts[0] == "tarreceive") && parts[1] == "apps" && parts[2] != "":
		return parts[2], true
	case len(parts) == 2 && parts[0] == "repos" && strings.HasSuffix(parts[1], ".tar") && parts[1] != ".tar":
		return strings.TrimSuffix(parts[1], ".tar"), true
	}
	return "", false
}
