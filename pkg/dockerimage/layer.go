package dockerimage

import (
	"compress/gzip"
	"io"
	"os"
)

func openLayer(path string) (io.ReadCloser, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	var header [2]byte
	if _, err := io.ReadFull(f, header[:]); err != nil {
		f.Close()
		return nil, err
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		f.Close()
		return nil, err
	}
	if header[0] != 0x1f || header[1] != 0x8b {
		return f, nil
	}
	gz, err := gzip.NewReader(f)
	if err != nil {
		f.Close()
		return nil, err
	}
	return &gzipReadCloser{gz: gz, f: f}, nil
}

type gzipReadCloser struct {
	gz *gzip.Reader
	f  *os.File
}

func (r *gzipReadCloser) Read(p []byte) (int, error) { return r.gz.Read(p) }

func (r *gzipReadCloser) Close() error {
	r.gz.Close()
	return r.f.Close()
}
