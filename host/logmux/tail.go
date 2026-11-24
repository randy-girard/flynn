package logmux

import (
	"bytes"
	"container/heap"
	"errors"
	"fmt"
	"io"
	"strconv"
	"time"

	"github.com/randy-girard/flynn/pkg/syslog/rfc6587"
)

// tailChunk is how much of a log file is read at a time going backward.
const tailChunk = 64 << 10

// maxFrameLen is the longest line a log file can hold: a five digit
// MSG-LEN, a space, the message and its newline.
const maxFrameLen = 5 + 1 + rfc6587.MaxMsgLen + 1

var errCorruptFrame = errors.New("no valid frame before this offset")

// backFrames reads the RFC6587 frames of a log file (each followed by a
// newline, as appLog writes them) from the end. Only the bytes of the
// current chunk and frame are held.
type backFrames struct {
	r       io.ReaderAt
	buf     []byte // file bytes [off, end)
	off     int64
	end     int64 // the next frame returned ends here, after its newline
	started bool
}

func newBackFrames(r io.ReaderAt, size int64) *backFrames {
	return &backFrames{r: r, off: size, end: size}
}

// load makes buf start at or before lo.
func (b *backFrames) load(lo int64) error {
	if lo >= b.off {
		return nil
	}
	b.buf = b.buf[:b.end-b.off]
	start := b.off - tailChunk
	if lo < start {
		start = lo
	}
	if start < 0 {
		start = 0
	}
	nb := make([]byte, b.off-start+int64(len(b.buf)))
	n, err := b.r.ReadAt(nb[:b.off-start], start)
	if int64(n) < b.off-start {
		if err == nil || err == io.EOF {
			err = io.ErrUnexpectedEOF
		}
		return err
	}
	copy(nb[b.off-start:], b.buf)
	b.buf, b.off = nb, start
	return nil
}

// prev returns the file offset and the message (without its MSG-LEN and
// newline) of the frame before the last one returned, or io.EOF at the
// start of the file. A partial line at the end of the file is skipped. The
// message is only valid until the next call.
func (b *backFrames) prev() (int64, []byte, error) {
	if !b.started {
		b.started = true
		// Drop a line still being written.
		for {
			if i := bytes.LastIndexByte(b.buf[:b.end-b.off], '\n'); i >= 0 {
				b.end = b.off + int64(i) + 1
				break
			}
			if b.off == 0 {
				b.end = 0
				break
			}
			if err := b.load(b.off - 1); err != nil {
				return 0, nil, err
			}
		}
	}
	if b.end == 0 {
		return 0, nil, io.EOF
	}
	// The frame's newline is at end-1. Its start is the file start or just
	// after an earlier newline; a message may itself hold a newline, so a
	// candidate counts only if its MSG-LEN ends the frame at end.
	pos := b.end - 1
	for {
		if b.end-pos > maxFrameLen {
			return 0, nil, errCorruptFrame
		}
		if pos-1 < b.off && b.off > 0 {
			if err := b.load(pos - 1); err != nil {
				return 0, nil, err
			}
		}
		var s int64
		if i := bytes.LastIndexByte(b.buf[:pos-b.off], '\n'); i >= 0 {
			s = b.off + int64(i) + 1
		} else if b.off == 0 {
			s = 0
		} else {
			if err := b.load(b.off - 1); err != nil {
				return 0, nil, err
			}
			continue
		}
		if msg, ok := frameAt(b.buf[s-b.off : b.end-b.off]); ok {
			b.end = s
			return s, msg, nil
		}
		if s == 0 {
			return 0, nil, errCorruptFrame
		}
		pos = s - 1
	}
}

// frameAt returns the message of f if f is exactly one frame: MSG-LEN, a
// space, MSG-LEN bytes and a newline.
func frameAt(f []byte) ([]byte, bool) {
	sp := bytes.IndexByte(f, ' ')
	if sp <= 0 || sp > 5 {
		return nil, false
	}
	n, err := strconv.Atoi(string(f[:sp]))
	if err != nil || n <= 0 || sp+1+n+1 != len(f) || f[len(f)-1] != '\n' {
		return nil, false
	}
	return f[sp+1 : len(f)-1], true
}

// timeHeap is a min-heap of line times: the n newest seen so far, the oldest
// of them on top.
type timeHeap []time.Time

func (h timeHeap) Len() int            { return len(h) }
func (h timeHeap) Less(i, j int) bool  { return h[i].Before(h[j]) }
func (h timeHeap) Swap(i, j int)       { h[i], h[j] = h[j], h[i] }
func (h *timeHeap) Push(x interface{}) { *h = append(*h, x.(time.Time)) }
func (h *timeHeap) Pop() interface{} {
	old := *h
	x := old[len(old)-1]
	*h = old[:len(old)-1]
	return x
}

// newest keeps the n newest times offered. n <= 0 keeps none and is never
// full.
type newest struct {
	n int
	h timeHeap
}

func (k *newest) full() bool { return k.n > 0 && len(k.h) >= k.n }

// oldest is the oldest of the n newest times (zero until full).
func (k *newest) oldest() time.Time {
	if !k.full() {
		return time.Time{}
	}
	return k.h[0]
}

func (k *newest) offer(t time.Time) {
	if k.n <= 0 {
		return
	}
	if len(k.h) < k.n {
		heap.Push(&k.h, t)
		return
	}
	if t.After(k.h[0]) {
		k.h[0] = t
		heap.Fix(&k.h, 0)
	}
}

func (k *newest) String() string { return fmt.Sprintf("newest(%d of %d)", len(k.h), k.n) }
