package main

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/randy-girard/flynn/controller/authorizer"
	"github.com/randy-girard/flynn/controller/authz"
	"github.com/randy-girard/flynn/controller/data"
	ct "github.com/randy-girard/flynn/controller/types"
	"github.com/randy-girard/flynn/pkg/ctxhelper"
	"github.com/randy-girard/flynn/pkg/httphelper"
	"github.com/randy-girard/flynn/pkg/sse"
	"golang.org/x/net/context"
)

func (c *controllerAPI) maybeStartEventListener() (*data.EventListener, error) {
	c.eventListenerMtx.Lock()
	defer c.eventListenerMtx.Unlock()
	if c.eventListener != nil && !c.eventListener.IsClosed() {
		return c.eventListener, nil
	}
	c.eventListener = data.NewEventListener(c.eventRepo)
	return c.eventListener, c.eventListener.Listen()
}

func (c *controllerAPI) GetEvent(ctx context.Context, w http.ResponseWriter, req *http.Request) {
	params, _ := ctxhelper.ParamsFromContext(ctx)
	id, err := strconv.ParseInt(params.ByName("id"), 10, 64)
	if err != nil {
		respondWithError(w, err)
		return
	}
	event, err := c.eventRepo.GetEvent(id)
	if err != nil {
		respondWithError(w, err)
		return
	}
	if hideInternalToken(ctx) && dropInternalEvent(event) {
		respondWithError(w, ErrNotFound)
		return
	}
	if !c.allowEventRead(ctx, w, req, nil, event.AppID) {
		return
	}
	httphelper.JSON(w, 200, presentEvent(ctx, nil, event))
}

func (c *controllerAPI) Events(ctx context.Context, w http.ResponseWriter, req *http.Request) {
	l, _ := ctxhelper.LoggerFromContext(ctx)
	log := l.New("fn", "Events")
	var app *ct.App
	if appID := req.FormValue("app_id"); appID != "" {
		data, err := c.appRepo.Get(appID)
		if err != nil {
			respondWithError(w, err)
			return
		}
		app = data.(*ct.App)
	}
	if !c.allowEventRead(ctx, w, req, app, "") {
		return
	}

	if req.Header.Get("Accept") == "application/json" {
		if err := listEvents(ctx, w, req, app, c.eventRepo); err != nil {
			log.Error("error listing events", "err", err)
			respondWithError(w, err)
		}
		return
	}

	eventListener, err := c.maybeStartEventListener()
	if err != nil {
		log.Error("error starting event listener", "err", err)
		respondWithError(w, err)
		return
	}
	if err := streamEvents(ctx, w, req, eventListener, app, c.eventRepo); err != nil {
		log.Error("error streaming events", "err", err)
		respondWithError(w, err)
	}
}

func listEvents(ctx context.Context, w http.ResponseWriter, req *http.Request, app *ct.App, repo *data.EventRepo) (err error) {
	var appIDs []string
	if app != nil {
		appIDs = []string{app.ID}
	}

	var beforeID *int64
	if req.FormValue("before_id") != "" {
		id, err := strconv.ParseInt(req.FormValue("before_id"), 10, 64)
		if err != nil {
			return ct.ValidationError{Field: "before_id", Message: "is invalid"}
		}
		beforeID = &id
	}

	var sinceID *int64
	if req.FormValue("since_id") != "" {
		id, err := strconv.ParseInt(req.FormValue("since_id"), 10, 64)
		if err != nil {
			return ct.ValidationError{Field: "since_id", Message: "is invalid"}
		}
		sinceID = &id
	}

	var count int
	if req.FormValue("count") != "" {
		count, err = strconv.Atoi(req.FormValue("count"))
		if err != nil {
			return ct.ValidationError{Field: "count", Message: "is invalid"}
		}
	}

	objectTypes := strings.Split(req.FormValue("object_types"), ",")
	if len(objectTypes) == 1 && objectTypes[0] == "" {
		objectTypes = []string{}
	}
	var objectIDs []string
	if id := req.FormValue("object_id"); id != "" {
		objectIDs = []string{id}
	}

	list, err := repo.ListEvents(appIDs, objectTypes, objectIDs, beforeID, sinceID, count)
	if err != nil {
		return err
	}
	httphelper.JSON(w, 200, presentEvents(ctx, app, list))
	return nil
}

// allowEventRead enforces events:read. Cluster admin, including the cluster
// key, may omit app_id and receives every app. A scoped token must name an
// app it is granted, and a mismatched account is denied even if the grant
// lists that app id. Release env is stripped later by presentEvent because
// events:read does not expand to app:env:read.
func (c *controllerAPI) allowEventRead(ctx context.Context, w http.ResponseWriter, req *http.Request, app *ct.App, eventAppID string) bool {
	tok := authz.TokenFromContext(ctx)
	if tok == nil || tok.HasClusterAdmin() {
		return true
	}
	if app == nil && eventAppID != "" && c.appRepo != nil {
		if data, err := c.appRepo.Get(eventAppID); err == nil {
			app, _ = data.(*ct.App)
		}
	}
	method, path := req.Method, req.URL.Path
	if app == nil {
		c.rejectScopedEvent(tok, method, path, "app_id required")
		httphelper.Forbidden(w, "app_id is required")
		return false
	}
	if !authz.EventsGrantAllows(tok, app.ID, app.Name, app.OwnerAccount) {
		c.rejectScopedEvent(tok, method, path, "app not granted")
		httphelper.Forbidden(w, "this credential is not allowed to read events for this app")
		return false
	}
	return true
}

func (c *controllerAPI) rejectScopedEvent(tok *authorizer.Token, method, path, reason string) {
	authz.NoteScopedReject(tok, method, path, reason)
	caller := ""
	scope := ""
	if tok != nil {
		caller = tok.UserID
		scope = strings.Join(tok.Scopes, ",")
	}
	logger.Info("scoped token rejected", "scope", scope, "caller", caller, "method", method, "path", path, "reason", reason)
}

func streamEvents(ctx context.Context, w http.ResponseWriter, req *http.Request, eventListener *data.EventListener, app *ct.App, repo *data.EventRepo) (err error) {
	var appIDs []string
	if app != nil {
		appIDs = []string{app.ID}
	}

	var lastID int64
	if req.Header.Get("Last-Event-Id") != "" {
		lastID, err = strconv.ParseInt(req.Header.Get("Last-Event-Id"), 10, 64)
		if err != nil {
			return ct.ValidationError{Field: "Last-Event-Id", Message: "is invalid"}
		}
	}

	var count int
	if req.FormValue("count") != "" {
		count, err = strconv.Atoi(req.FormValue("count"))
		if err != nil {
			return ct.ValidationError{Field: "count", Message: "is invalid"}
		}
	}

	objectTypes := strings.Split(req.FormValue("object_types"), ",")
	if len(objectTypes) == 1 && objectTypes[0] == "" {
		objectTypes = []string{}
	}
	var objectIDs []string
	if id := req.FormValue("object_id"); id != "" {
		objectIDs = []string{id}
	}
	past := req.FormValue("past")

	hide := hideInternal(ctx, app) || (app == nil && hideInternalToken(ctx))
	l, _ := ctxhelper.LoggerFromContext(ctx)
	log := l.New("fn", "streamEvents", "object_types", objectTypes, "object_ids", objectIDs)

	// Subscribe before the response headers go out: a client that opens the
	// stream and then lists (the router) must get every change made after
	// it has the headers.
	sub, err := eventListener.Subscribe(appIDs, objectTypes, objectIDs)
	if err != nil {
		return err
	}
	defer sub.Close()

	// Tell the client which id the stream starts after (EDGE-94). Events
	// with a larger id come on this stream; the replay below covers those
	// up to now and the subscription the rest. A client that sees no event
	// before the stream drops resumes from this id instead of 0, for which
	// nothing is replayed. A stream that replays from the beginning has no
	// start id.
	switch {
	case lastID > 0:
		w.Header().Set(sse.LastEventIDHeader, strconv.FormatInt(lastID, 10))
	case past != "true":
		startID, err := repo.LastID()
		if err != nil {
			return err
		}
		w.Header().Set(sse.LastEventIDHeader, strconv.FormatInt(startID, 10))
	}

	ch := make(chan *ct.Event)
	s := sse.NewStream(w, ch, log)
	s.Serve()
	defer func() {
		if err == nil {
			s.Close()
		} else {
			s.CloseWithError(err)
		}
	}()

	var currID int64
	if past == "true" || lastID > 0 {
		list, err := repo.ListEvents(appIDs, objectTypes, objectIDs, nil, &lastID, count)
		if err != nil {
			return err
		}
		// events are in ID DESC order, so iterate in reverse
		for i := len(list) - 1; i >= 0; i-- {
			e := list[i]
			if hide && dropInternalEvent(e) {
				continue
			}
			e = presentEvent(ctx, app, e)
			// the SSE writer stops reading ch once the client has gone,
			// so every send must also watch s.Done (CORE-44)
			select {
			case <-s.Done:
				return nil
			case ch <- e:
			}
			currID = e.ID
		}
	}

	for {
		select {
		case <-s.Done:
			return
		case event, ok := <-sub.Events:
			if !ok {
				return sub.Err
			}
			if event.ID <= currID {
				continue
			}
			if hide && dropInternalEvent(event) {
				continue
			}
			select {
			case <-s.Done:
				return nil
			case ch <- presentEvent(ctx, app, event):
			}
		}
	}
}
