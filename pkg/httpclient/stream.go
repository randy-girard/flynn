package httpclient

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"strconv"
	"sync"
	"time"

	"github.com/randy-girard/flynn/pkg/attempt"
	"github.com/randy-girard/flynn/pkg/sse"
	"github.com/randy-girard/flynn/pkg/stream"
)

/*
Stream manufactures a `pkg/stream.Stream`, starts a worker pumping events out of decoding, and returns that.

The 'outputCh' parameter must be a sendable channel.  The "zero"-values of channel's content type will be created and used in the deserialization, then sent.

The return values from `httpclient.RawReq` are probably a useful starting point for the 'res' parameter.

Closing the returned `stream.Stream` shuts down the worker.
*/
func Stream(res *http.Response, outputCh interface{}) stream.Stream {
	stream := stream.New()

	var chanValue reflect.Value
	if v, ok := outputCh.(reflect.Value); ok {
		chanValue = v
	} else {
		chanValue = reflect.ValueOf(outputCh)
	}

	stopChanValue := reflect.ValueOf(stream.StopCh)
	msgType := chanValue.Type().Elem().Elem()
	go func() {
		done := make(chan struct{})
		defer func() {
			chanValue.Close()
			close(done)
		}()

		go func() {
			select {
			case <-stream.StopCh:
			case <-done:
			}
			res.Body.Close()
		}()

		r := bufio.NewReader(res.Body)
		dec := sse.NewDecoder(r)
		for {
			msg := reflect.New(msgType)
			if err := dec.Decode(msg.Interface()); err != nil {
				if err != io.EOF {
					stream.Error = err
				}
				break
			}
			chosen, _, _ := reflect.Select([]reflect.SelectCase{
				{
					Dir:  reflect.SelectRecv,
					Chan: stopChanValue,
				},
				{
					Dir:  reflect.SelectSend,
					Chan: chanValue,
					Send: msg,
				},
			})
			switch chosen {
			case 0:
				return
			default:
			}
		}
	}()
	return stream
}

// Reconnection budget for SSE streams (e.g. controller /events) after a read
// error or disconnect. Longer than the legacy 20s windows so updates survive
// controller/postgres load spikes during system app rolls.
var connectAttempts = attempt.Strategy{
	Total: 15 * time.Minute,
	Delay: 100 * time.Millisecond,
}

// ResumingStream streams events into outputCh and reconnects after the
// connection drops, for up to connectAttempts.Total, asking the server for
// the events after the last one it sent (the Last-Event-Id header, read from
// an int64 ID field of the event type). Before the first event that cursor
// is the stream's start id from the sse.LastEventIDHeader response header,
// so a reconnect before any event still resumes where the stream began.
// Against a server that sends no start id the cursor stays 0 until the first
// event, and the server replays nothing for 0: a caller that must not miss
// a change in that gap uses ResumingStreamWithOptions with EndOnDisconnect.
func ResumingStream(connect func(int64) (*http.Response, error, bool), outputCh interface{}) (stream.Stream, error) {
	return ResumingStreamWithOptions(connect, outputCh, StreamOptions{})
}

// ErrStreamDisconnected ends a stream opened with EndOnDisconnect when its
// connection drops.
var ErrStreamDisconnected = errors.New("httpclient: event stream disconnected")

// StreamOptions changes how a resuming stream handles a dropped connection.
type StreamOptions struct {
	// EndOnDisconnect ends the stream with ErrStreamDisconnected when the
	// connection drops, instead of reconnecting. The first connect is still
	// retried. A caller that builds state from a list uses it to list again.
	EndOnDisconnect bool
}

func ResumingStreamWithOptions(connect func(int64) (*http.Response, error, bool), outputCh interface{}, opts StreamOptions) (stream.Stream, error) {
	stream := stream.New()
	firstErr := make(chan error)
	go func() {
		var once sync.Once
		var lastID int64
		stopChanValue := reflect.ValueOf(stream.StopCh)
		outValue := reflect.ValueOf(outputCh)
		defer outValue.Close()
		for {
			var res *http.Response
			// nonRetryableErr will be set if a connection attempt should not
			// be retried (for example if a 404 is returned).
			var nonRetryableErr error
			err := connectAttempts.Run(func() (err error) {
				var retry bool
				res, err, retry = connect(lastID)
				if !retry {
					nonRetryableErr = err
					return nil
				}
				return
			})
			if nonRetryableErr != nil {
				err = nonRetryableErr
			}
			once.Do(func() { firstErr <- err })
			if err != nil {
				stream.Error = err
				return
			}
			if lastID == 0 {
				if start, err := strconv.ParseInt(res.Header.Get(sse.LastEventIDHeader), 10, 64); err == nil && start > 0 {
					lastID = start
				}
			}
			chanValue := reflect.MakeChan(outValue.Type(), 0)
			s := Stream(res, chanValue)
		loop:
			for {
				chosen, v, ok := reflect.Select([]reflect.SelectCase{
					{
						Dir:  reflect.SelectRecv,
						Chan: stopChanValue,
					},
					{
						Dir:  reflect.SelectRecv,
						Chan: chanValue,
					},
				})
				switch chosen {
				case 0:
					s.Close()
					return
				default:
					if !ok {
						// TODO: check s.Err() for a special error sent from the
						//       server indicating the stream should not be retried
						break loop
					}
					id := v.Elem().FieldByName("ID")
					if id.Kind() == reflect.Int64 {
						lastID = id.Int()
					}
					outValue.Send(v)
				}
			}
			if opts.EndOnDisconnect {
				if err := s.Err(); err != nil {
					stream.Error = fmt.Errorf("%w: %v", ErrStreamDisconnected, err)
				} else {
					stream.Error = ErrStreamDisconnected
				}
				return
			}
		}
	}()
	return stream, <-firstErr
}
