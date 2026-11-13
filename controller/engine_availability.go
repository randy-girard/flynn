package main

import (
	"net/http"

	"github.com/randy-girard/flynn/controller/data"
	"github.com/randy-girard/flynn/controller/tenancy"
	ct "github.com/randy-girard/flynn/controller/types"
	"github.com/randy-girard/flynn/pkg/httphelper"
	"golang.org/x/net/context"
)

func (c *controllerAPI) GetMySQLAvailability(ctx context.Context, w http.ResponseWriter, req *http.Request) {
	if c == nil || c.tenancy == nil {
		respondWithError(w, data.ErrNotFound)
		return
	}
	mode, err := c.tenancy.GetMySQLAvailability()
	if err != nil {
		respondWithError(w, err)
		return
	}
	httphelper.JSON(w, http.StatusOK, ct.MySQLAvailability{Mode: mode})
}

func (c *controllerAPI) PutMySQLAvailability(ctx context.Context, w http.ResponseWriter, req *http.Request) {
	var body ct.MySQLAvailability
	if err := httphelper.DecodeJSON(req, &body); err != nil {
		respondWithError(w, err)
		return
	}
	if err := tenancy.ValidateAvailability(body.Mode); err != nil {
		httphelper.ValidationError(w, "mode", err.Error())
		return
	}
	if c == nil || c.tenancy == nil {
		respondWithError(w, data.ErrNotFound)
		return
	}
	if err := c.tenancy.SetMySQLAvailability(body.Mode); err != nil {
		respondWithError(w, err)
		return
	}
	c.GetMySQLAvailability(ctx, w, req)
}

func (c *controllerAPI) GetRedisAvailability(ctx context.Context, w http.ResponseWriter, req *http.Request) {
	if c == nil || c.tenancy == nil {
		respondWithError(w, data.ErrNotFound)
		return
	}
	mode, err := c.tenancy.GetRedisAvailability()
	if err != nil {
		respondWithError(w, err)
		return
	}
	httphelper.JSON(w, http.StatusOK, ct.RedisAvailability{Mode: mode})
}

func (c *controllerAPI) PutRedisAvailability(ctx context.Context, w http.ResponseWriter, req *http.Request) {
	var body ct.RedisAvailability
	if err := httphelper.DecodeJSON(req, &body); err != nil {
		respondWithError(w, err)
		return
	}
	if err := tenancy.ValidateAvailability(body.Mode); err != nil {
		httphelper.ValidationError(w, "mode", err.Error())
		return
	}
	if c == nil || c.tenancy == nil {
		respondWithError(w, data.ErrNotFound)
		return
	}
	if err := c.tenancy.SetRedisAvailability(body.Mode); err != nil {
		respondWithError(w, err)
		return
	}
	c.GetRedisAvailability(ctx, w, req)
}
