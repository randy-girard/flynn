package tokensource

import (
	"fmt"
	"net/http"
	"strings"
	"sync"

	"github.com/randy-girard/flynn/cli/login/internal/oauth"
	"golang.org/x/oauth2"
)

func New(clusterName, issuer, controllerURL string, cache Cache, hc *http.Client) (oauth2.TokenSource, error) {
	metadataURL, clientID, err := oauth.BuildMetadataURL(issuer)
	if err != nil {
		return nil, err
	}
	if clientID == "" {
		clientID = "flynn-cli"
	}

	t, err := cache.GetToken(clusterName, clientID, controllerURL)
	if err != nil {
		return nil, err
	}

	return &tokenSource{
		clusterName:   clusterName,
		issuer:        issuer,
		controllerURL: controllerURL,
		metadataURL:   metadataURL,
		cache:         cache,
		httpClient:    hc,
		config:        &oauth2.Config{ClientID: clientID},
		t:             t,
	}, nil
}

type tokenSource struct {
	clusterName   string
	issuer        string
	controllerURL string
	metadataURL   string
	cache         Cache
	httpClient    *http.Client

	mtx    sync.Mutex
	config *oauth2.Config
	t      *oauth2.Token
}

func (s *tokenSource) metadataURLs() []string {
	seen := map[string]struct{}{}
	var out []string
	add := func(u string) {
		u = strings.TrimSpace(u)
		if u == "" {
			return
		}
		if _, ok := seen[u]; ok {
			return
		}
		seen[u] = struct{}{}
		out = append(out, u)
	}
	add(s.metadataURL)
	if u, _, err := oauth.BuildMetadataURL(s.controllerURL); err == nil {
		add(u)
	}
	return out
}

func (s *tokenSource) discover() error {
	var last error
	for _, u := range s.metadataURLs() {
		meta, err := oauth.GetMetadata(s.httpClient, u)
		if err != nil {
			last = err
			continue
		}
		s.config.Endpoint = oauth2.Endpoint{
			AuthStyle: oauth2.AuthStyleInParams,
			AuthURL:   meta.AuthorizationEndpoint,
			TokenURL:  meta.TokenEndpoint,
		}
		return nil
	}
	if last != nil {
		return last
	}
	return fmt.Errorf("oauth discovery failed")
}

func (s *tokenSource) Token() (*oauth2.Token, error) {
	s.mtx.Lock()
	defer s.mtx.Unlock()

	if s.t.Valid() {
		return s.t, nil
	}

	if s.config.Endpoint.TokenURL == "" {
		if err := s.discover(); err != nil {
			return nil, err
		}
	}
	audience, ok := s.t.Extra("audience").(string)
	if !ok {
		return nil, fmt.Errorf("token is missing audience parameter")
	}
	newToken, err := oauth.RefreshToken(s.httpClient, s.config, s.t, audience)
	if err != nil {
		return nil, fmt.Errorf("error refreshing token: %s", err)
	}

	if err := s.cache.SetToken(s.clusterName, s.config.ClientID, newToken); err != nil {
		return nil, err
	}
	s.t = newToken
	return newToken, nil
}
