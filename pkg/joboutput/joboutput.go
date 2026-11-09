// Package joboutput gzippes one-shot deploy transcripts so the controller
// database can keep them without storing the plain text.
package joboutput

import (
	"bytes"
	"compress/gzip"
	"io"
)

// MaxBytes is the most uncompressed text kept for one job or release.
// The tail is what a reader needs: the end of the build, the release
// command, and the deployed line.
const MaxBytes = 4 << 20

// Tail keeps the last max bytes of s, starting at a line boundary when
// the cut lands in the middle of a line.
func Tail(s string, max int) string {
	if max <= 0 {
		return ""
	}
	if len(s) <= max {
		return s
	}
	b := []byte(s)
	b = b[len(b)-max:]
	if i := bytes.IndexByte(b, '\n'); i >= 0 && i+1 < len(b) {
		b = b[i+1:]
	}
	return string(b)
}

// Compress gzip-encodes plain text.
func Compress(plain []byte) ([]byte, error) {
	var buf bytes.Buffer
	zw, err := gzip.NewWriterLevel(&buf, gzip.DefaultCompression)
	if err != nil {
		return nil, err
	}
	if _, err := zw.Write(plain); err != nil {
		_ = zw.Close()
		return nil, err
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// Decompress reads a gzip body written by Compress. It stops at MaxBytes+1
// so a corrupt length cannot expand without limit.
func Decompress(body []byte) ([]byte, error) {
	zr, err := gzip.NewReader(bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	defer zr.Close()
	return io.ReadAll(io.LimitReader(zr, int64(MaxBytes)+1))
}
