package main

import (
	"net/http"

	"github.com/randy-girard/flynn/controller/data"
	"github.com/randy-girard/flynn/controller/tenancy"
	ct "github.com/randy-girard/flynn/controller/types"
	"github.com/randy-girard/flynn/pkg/httphelper"
	"golang.org/x/net/context"
)

func (c *controllerAPI) GetPostgresAvailability(ctx context.Context, w http.ResponseWriter, req *http.Request) {
	if c == nil || c.tenancy == nil {
		respondWithError(w, data.ErrNotFound)
		return
	}
	mode, err := c.tenancy.GetPostgresAvailability()
	if err != nil {
		respondWithError(w, err)
		return
	}
	httphelper.JSON(w, http.StatusOK, ct.PostgresAvailability{Mode: mode})
}

func (c *controllerAPI) PutPostgresAvailability(ctx context.Context, w http.ResponseWriter, req *http.Request) {
	var body ct.PostgresAvailability
	if err := httphelper.DecodeJSON(req, &body); err != nil {
		respondWithError(w, err)
		return
	}
	if err := tenancy.ValidatePostgresAvailability(body.Mode); err != nil {
		httphelper.ValidationError(w, "mode", err.Error())
		return
	}
	if c == nil || c.tenancy == nil {
		respondWithError(w, data.ErrNotFound)
		return
	}
	if err := c.tenancy.SetPostgresAvailability(body.Mode); err != nil {
		respondWithError(w, err)
		return
	}
	c.GetPostgresAvailability(ctx, w, req)
}
