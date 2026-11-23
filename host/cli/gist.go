package cli

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"net/http"
	"os"
	"sort"
	"time"

	"github.com/cheggaaa/pb"
	"github.com/inconshreveable/log15"
	"github.com/randy-girard/flynn/pkg/backup"
	"github.com/randy-girard/flynn/pkg/term"
)

// Actual limit is likely ~200mb.
const GistMaxSize = 195 * 1024 * 1024

// gistAPIURL is where Upload posts. Tests point it at a local stub.
var gistAPIURL = "https://api.github.com/gists"

type Gist struct {
	URL         string          `json:"html_url,omitempty"`
	Description string          `json:"description"`
	Public      bool            `json:"public"`
	Files       map[string]File `json:"files"`
	Size        int
}

func (g *Gist) AddLocalFile(name, filepath string) error {
	content, err := ioutil.ReadFile(filepath)
	if err != nil {
		return err
	}
	g.AddFile(name, string(content))
	return nil
}

func (g *Gist) AddFile(name, content string) {
	g.Files[name] = File{Content: content}
	g.Size = g.Size + len(content)
}

// Redact applies r to every file's content and returns the number of files
// that changed. Size is recomputed from the redacted content.
func (g *Gist) Redact(r *debugRedactor) int {
	g.Size = 0
	changed := 0
	for name, file := range g.Files {
		redacted := r.Redact(file.Content)
		if redacted != file.Content {
			changed++
		}
		file.Content = redacted
		g.Files[name] = file
		g.Size += len(file.Content)
	}
	return changed
}

// Names returns the file names in sorted order.
func (g *Gist) Names() []string {
	names := make([]string, 0, len(g.Files))
	for name := range g.Files {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func (g *Gist) Upload(log log15.Logger, token string) error {
	if len(g.Files) == 0 {
		return errors.New("cannot create empty gist")
	}

	payload, err := json.Marshal(g)
	if err != nil {
		log.Error("error preparing gist content", "err", err)
		return err
	}
	var body io.Reader = bytes.NewReader(payload)
	if term.IsTerminal(os.Stderr.Fd()) {
		bar := pb.New(len(payload))
		bar.SetUnits(pb.U_BYTES)
		bar.ShowSpeed = true
		bar.Output = os.Stderr
		bar.Start()
		defer bar.Finish()
		body = bar.NewProxyReader(body)
	}
	req, err := http.NewRequest("POST", gistAPIURL, body)
	if err != nil {
		log.Error("error preparing HTTP request", "err", err)
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "token "+token)
		log.Info("creating authenticated gist")
	} else {
		log.Info("creating anonymous gist")
	}

	res, err := (&http.Client{Timeout: 10 * time.Minute}).Do(req)
	if err != nil {
		log.Error("error uploading gist content", "err", err)
		return err
	}
	defer res.Body.Close()

	if res.StatusCode != http.StatusCreated {
		e := fmt.Sprintf("unexpected HTTP status: %d", res.StatusCode)
		log.Error(e)
		return errors.New(e)
	}
	if err := json.NewDecoder(res.Body).Decode(g); err != nil {
		log.Error("error decoding HTTP response", "err", err)
		return err
	}
	return nil
}

// CreateTarball writes the files to a gzipped tarball at path. The file is
// created owner-only (0600) and replaced atomically; a failed write or close
// is returned and leaves no partial file at path.
func (g *Gist) CreateTarball(path string) error {
	return backup.WriteFileAtomic(path, g.WriteTarball)
}

// WriteTarball writes the files to w as a gzipped tarball, checking the tar
// and gzip close errors (the gzip trailer is only written on Close).
func (g *Gist) WriteTarball(w io.Writer) error {
	gz, err := gzip.NewWriterLevel(w, gzip.BestCompression)
	if err != nil {
		return err
	}
	tarball := tar.NewWriter(gz)
	now := time.Now()
	for _, name := range g.Names() {
		file := g.Files[name]
		hdr := &tar.Header{
			Name:    name,
			Mode:    0600,
			ModTime: now,
			Size:    int64(len(file.Content)),
		}
		if err := tarball.WriteHeader(hdr); err != nil {
			gz.Close()
			return err
		}
		if _, err := tarball.Write([]byte(file.Content)); err != nil {
			gz.Close()
			return err
		}
	}
	if err := tarball.Close(); err != nil {
		gz.Close()
		return err
	}
	return gz.Close()
}

type File struct {
	Content string `json:"content"`
}
