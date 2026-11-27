package main

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/randy-girard/flynn/controller/schema"
	ct "github.com/randy-girard/flynn/controller/types"
	"github.com/randy-girard/flynn/pkg/ctxhelper"
	"github.com/randy-girard/flynn/pkg/httphelper"
	"github.com/randy-girard/flynn/pkg/sse"
	"golang.org/x/net/context"
)

func (c *controllerAPI) GetVolumes(ctx context.Context, w http.ResponseWriter, req *http.Request) {
	if strings.Contains(req.Header.Get("Accept"), "text/event-stream") {
		c.streamVolumes(ctx, w, req)
		return
	}

	if truthyQuery(req.FormValue("orphaned")) {
		c.getOrphanedVolumes(w, req)
		return
	}

	list, err := c.volumeRepo.List()
	if err != nil {
		respondWithError(w, err)
		return
	}
	httphelper.JSON(w, 200, list)
}

// defaultOrphanedVolumeGrace is how long ago an app must have been deleted
// before GET /volumes?orphaned=true lists its volumes.
const defaultOrphanedVolumeGrace = 24 * time.Hour

// getOrphanedVolumes lists volumes of deleted apps that were never
// decommissioned (NEW-35). grace is a Go duration (default 24h).
func (c *controllerAPI) getOrphanedVolumes(w http.ResponseWriter, req *http.Request) {
	grace := defaultOrphanedVolumeGrace
	if g := strings.TrimSpace(req.FormValue("grace")); g != "" {
		d, err := time.ParseDuration(g)
		if err != nil || d < 0 {
			respondWithError(w, ct.ValidationError{Field: "grace", Message: "must be a non-negative duration such as 24h"})
			return
		}
		grace = d
	}
	list, err := c.volumeRepo.ListOrphaned(grace)
	if err != nil {
		respondWithError(w, err)
		return
	}
	if list == nil {
		list = []*ct.Volume{}
	}
	httphelper.JSON(w, 200, list)
}

func (c *controllerAPI) GetAppVolumes(ctx context.Context, w http.ResponseWriter, req *http.Request) {
	list, err := c.volumeRepo.AppList(c.getApp(ctx).ID)
	if err != nil {
		respondWithError(w, err)
		return
	}
	httphelper.JSON(w, 200, list)
}

func (c *controllerAPI) GetVolume(ctx context.Context, w http.ResponseWriter, req *http.Request) {
	params, _ := ctxhelper.ParamsFromContext(ctx)
	volume, err := c.volumeRepo.Get(c.getApp(ctx).ID, params.ByName("volume_id"))
	if err != nil {
		respondWithError(w, err)
		return
	}
	httphelper.JSON(w, 200, volume)
}

func (c *controllerAPI) PutVolume(ctx context.Context, w http.ResponseWriter, req *http.Request) {
	var volume ct.Volume
	if err := httphelper.DecodeJSON(req, &volume); err != nil {
		respondWithError(w, err)
		return
	}

	if err := schema.Validate(volume); err != nil {
		respondWithError(w, err)
		return
	}

	if err := c.volumeRepo.Add(&volume); err != nil {
		respondWithError(w, err)
		return
	}
	httphelper.JSON(w, 200, &volume)
}

func (c *controllerAPI) DecommissionVolume(ctx context.Context, w http.ResponseWriter, req *http.Request) {
	var volume ct.Volume
	if err := httphelper.DecodeJSON(req, &volume); err != nil {
		respondWithError(w, err)
		return
	}
	if err := c.volumeRepo.Decommission(c.getApp(ctx).ID, &volume); err != nil {
		respondWithError(w, err)
		return
	}
	httphelper.JSON(w, 200, &volume)
}

func (c *controllerAPI) streamVolumes(ctx context.Context, w http.ResponseWriter, req *http.Request) (err error) {
	l, _ := ctxhelper.LoggerFromContext(ctx)
	ch := make(chan *ct.Volume)
	stream := sse.NewStream(w, ch, l)
	stream.Serve()
	defer func() {
		if err == nil {
			stream.Close()
		} else {
			stream.CloseWithError(err)
		}
	}()

	since, err := time.Parse(time.RFC3339Nano, req.FormValue("since"))
	if err != nil {
		return err
	}

	eventListener, err := c.maybeStartEventListener()
	if err != nil {
		l.Error("error starting event listener")
		return err
	}

	sub, err := eventListener.Subscribe(nil, []string{string(ct.EventTypeVolume)}, nil)
	if err != nil {
		return err
	}
	defer sub.Close()

	vols, err := c.volumeRepo.ListSince(since)
	if err != nil {
		return err
	}
	currentUpdatedAt := since
	for _, vol := range vols {
		select {
		case <-stream.Done:
			return nil
		case ch <- vol:
			if vol.UpdatedAt.After(currentUpdatedAt) {
				currentUpdatedAt = *vol.UpdatedAt
			}
		}
	}

	select {
	case <-stream.Done:
		return nil
	case ch <- &ct.Volume{}:
	}

	for {
		select {
		case <-stream.Done:
			return
		case event, ok := <-sub.Events:
			if !ok {
				return sub.Err
			}
			var vol ct.Volume
			if err := json.Unmarshal(event.Data, &vol); err != nil {
				l.Error("error deserializing volume event", "event.id", event.ID, "err", err)
				continue
			}
			if vol.UpdatedAt.Before(currentUpdatedAt) {
				continue
			}
			select {
			case <-stream.Done:
				return nil
			case ch <- &vol:
			}
		}
	}
}
