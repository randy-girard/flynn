package main

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/randy-girard/flynn/controller/data"
	ct "github.com/randy-girard/flynn/controller/types"
	"github.com/randy-girard/flynn/pkg/ctxhelper"
	"github.com/randy-girard/flynn/pkg/httphelper"
	"github.com/randy-girard/flynn/pkg/sse"
	"golang.org/x/net/context"
)

func (c *controllerAPI) GetManagedCertificates(ctx context.Context, w http.ResponseWriter, req *http.Request) {
	if strings.Contains(req.Header.Get("Accept"), "text/event-stream") {
		c.streamManagedCertificates(ctx, w, req)
		return
	}

	certs, err := c.listManagedCertificates(req)
	if err != nil {
		if ve, ok := err.(listManagedCertificatesError); ok {
			httphelper.ValidationError(w, ve.field, ve.reason)
			return
		}
		respondWithError(w, err)
		return
	}
	httphelper.JSON(w, 200, certs)
}

type listManagedCertificatesError struct {
	field, reason string
}

func (e listManagedCertificatesError) Error() string {
	return e.field + ": " + e.reason
}

func (c *controllerAPI) listManagedCertificates(req *http.Request) ([]*ct.ManagedCertificate, error) {
	q := req.URL.Query()
	if beforeParam := q.Get("expiring_before"); beforeParam != "" {
		before, err := time.Parse(time.RFC3339Nano, beforeParam)
		if err != nil {
			before, err = time.Parse(time.RFC3339, beforeParam)
		}
		if err != nil {
			return nil, listManagedCertificatesError{field: "expiring_before", reason: "must be a valid RFC3339 timestamp"}
		}
		return c.managedCertificateRepo.ListExpiring(before)
	}
	if statusParam := q.Get("status"); statusParam != "" {
		status := ct.ManagedCertificateStatus(statusParam)
		switch status {
		case ct.ManagedCertificateStatusPending, ct.ManagedCertificateStatusIssued, ct.ManagedCertificateStatusFailed, ct.ManagedCertificateStatusRenewing:
		default:
			return nil, listManagedCertificatesError{field: "status", reason: "must be pending, issued, failed, or renewing"}
		}
		return c.managedCertificateRepo.ListByStatus(status)
	}
	if sinceParam := q.Get("since"); sinceParam != "" {
		since, err := time.Parse(time.RFC3339Nano, sinceParam)
		if err != nil {
			return nil, listManagedCertificatesError{field: "since", reason: "must be a valid RFC3339 timestamp"}
		}
		return c.managedCertificateRepo.ListSince(since)
	}
	return c.managedCertificateRepo.List()
}

func (c *controllerAPI) streamManagedCertificates(ctx context.Context, w http.ResponseWriter, req *http.Request) (err error) {
	l, _ := ctxhelper.LoggerFromContext(ctx)
	ch := make(chan *ct.ManagedCertificate)
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

	sub, err := eventListener.Subscribe(nil, []string{string(ct.EventTypeManagedCertificate)}, nil)
	if err != nil {
		return err
	}
	defer sub.Close()

	certs, err := c.managedCertificateRepo.ListSince(since)
	if err != nil {
		l.Error("error listing managed certificates", "err", err)
		return err
	}
	l.Info("streaming managed certificates", "count", len(certs), "since", since)
	currentUpdatedAt := since
	for _, cert := range certs {
		select {
		case <-stream.Done:
			l.Info("stream done while sending initial certificates")
			return nil
		case ch <- cert:
			if cert.UpdatedAt != nil && cert.UpdatedAt.After(currentUpdatedAt) {
				currentUpdatedAt = *cert.UpdatedAt
			}
		}
	}

	// Send an empty marker to indicate initial list is complete
	select {
	case <-stream.Done:
		l.Info("stream done while sending marker")
		return nil
	case ch <- &ct.ManagedCertificate{}:
	}
	l.Info("sent initial certificate list and marker, waiting for events")

	for {
		select {
		case <-stream.Done:
			l.Info("stream done while waiting for events")
			return
		case event, ok := <-sub.Events:
			if !ok {
				l.Error("event subscription closed", "err", sub.Err)
				return sub.Err
			}
			var cert ct.ManagedCertificate
			if err := json.Unmarshal(event.Data, &cert); err != nil {
				l.Error("error deserializing managed certificate event", "event.id", event.ID, "err", err)
				continue
			}
			if cert.UpdatedAt.Before(currentUpdatedAt) {
				continue
			}
			select {
			case <-stream.Done:
				return nil
			case ch <- &cert:
			}
		}
	}
}

func (c *controllerAPI) GetManagedCertificate(ctx context.Context, w http.ResponseWriter, req *http.Request) {
	params, _ := ctxhelper.ParamsFromContext(ctx)
	certID := params.ByName("managed_certificate_id")

	cert, err := c.managedCertificateRepo.Get(certID)
	if err != nil {
		if err == data.ErrNotFound {
			err = ErrNotFound
		}
		respondWithError(w, err)
		return
	}
	httphelper.JSON(w, 200, cert)
}

func (c *controllerAPI) UpdateManagedCertificate(ctx context.Context, w http.ResponseWriter, req *http.Request) {
	params, _ := ctxhelper.ParamsFromContext(ctx)
	certID := params.ByName("managed_certificate_id")

	var cert ct.ManagedCertificate
	if err := httphelper.DecodeJSON(req, &cert); err != nil {
		respondWithError(w, err)
		return
	}
	cert.ID = certID

	if err := c.managedCertificateRepo.Update(&cert); err != nil {
		if err == data.ErrNotFound {
			err = ErrNotFound
		}
		respondWithError(w, err)
		return
	}
	httphelper.JSON(w, 200, cert)
}
