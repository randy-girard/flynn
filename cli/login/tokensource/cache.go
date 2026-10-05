package tokensource

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/randy-girard/flynn/cli/login/internal/oauth"
	"github.com/randy-girard/flynn/pkg/lockedfile"
	"golang.org/x/oauth2"
)

type Cache interface {
	GetToken(clusterName, clientID, audience string) (*oauth2.Token, error)
	SetToken(clusterName, clientID string, t *oauth2.Token) error
}

func NewTokenCache(dir string) Cache {
	return &cache{baseDir: dir}
}

type cache struct {
	baseDir string
}

type tokenCache struct {
	RefreshToken          string    `json:"refresh_token"`
	RefreshTokenExpiry    time.Time `json:"refresh_token_expiry"`
	RefreshTokenIssueTime time.Time `json:"refresh_token_issue_time"`

	AccessTokens map[string]*accessToken `json:"access_tokens"`
}

type accessToken struct {
	AccessToken string    `json:"access_token"`
	Expiry      time.Time `json:"expiry"`
	TokenType   string    `json:"token_type"`
}

func clusterCacheName(clusterName string) (string, error) {
	name := strings.TrimSpace(clusterName)
	if name == "" {
		return "", ErrTokenNotFound
	}
	if strings.ContainsAny(name, `/\`) || strings.Contains(name, "..") {
		return "", fmt.Errorf("invalid cluster name for token cache")
	}
	return name, nil
}

func (c *cache) filepath(clusterName, clientID string) (string, string, error) {
	name, err := clusterCacheName(clusterName)
	if err != nil {
		return "", "", err
	}
	if strings.TrimSpace(clientID) == "" {
		return "", "", fmt.Errorf("invalid OAuth client id")
	}
	return filepath.Join(c.baseDir, name), clientID + ".json", nil
}

var ErrTokenNotFound = errors.New("cached token not found")

func (c *cache) readCache(path string) (*tokenCache, error) {
	data, err := lockedfile.Read(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrTokenNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("error reading token cache %q: %s", path, err)
	}
	res := &tokenCache{}
	if err := json.Unmarshal(data, res); err != nil {
		return nil, fmt.Errorf("error decoding token cache %q: %s", path, err)
	}
	return res, nil
}

func (c *cache) GetToken(clusterName, clientID, audience string) (*oauth2.Token, error) {
	dir, filename, err := c.filepath(clusterName, clientID)
	if err != nil {
		return nil, err
	}
	cache, err := c.readCache(filepath.Join(dir, filename))
	if err != nil {
		return nil, err
	}

	if cache.RefreshToken == "" {
		return nil, ErrTokenNotFound
	}

	t := &oauth2.Token{
		RefreshToken: cache.RefreshToken,
	}
	if audience == "" {
		t.AccessToken = cache.RefreshToken
		t.TokenType = "RefreshToken"
		t.Expiry = cache.RefreshTokenExpiry
	} else {
		at, ok := cache.AccessTokens[audience]
		if !ok || at.AccessToken == "" {
			return nil, ErrTokenNotFound
		}
		t.AccessToken = at.AccessToken
		t.TokenType = at.TokenType
		t.Expiry = at.Expiry
	}
	t = t.WithExtra(map[string]interface{}{
		oauth.RefreshTokenExpiry:    cache.RefreshTokenExpiry,
		oauth.RefreshTokenIssueTime: cache.RefreshTokenIssueTime,
		"audience":                  audience,
	})

	return t, nil
}

func (c *cache) SetToken(clusterName, clientID string, t *oauth2.Token) error {
	dir, filename, err := c.filepath(clusterName, clientID)
	if err != nil {
		return err
	}

	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("error creating token cache directory %q: %s", dir, err)
	}

	cacheFilePath := filepath.Join(dir, filename)
	return lockedfile.Transform(cacheFilePath, 0600, func(old []byte) ([]byte, error) {
		cached := &tokenCache{}
		if len(old) > 0 {
			if err := json.Unmarshal(old, cached); err != nil {
				return nil, fmt.Errorf("error decoding existing token cache %q: %s", cacheFilePath, err)
			}
		}
		if cached.AccessTokens == nil {
			cached.AccessTokens = make(map[string]*accessToken)
		}

		updatedRefresh := false
		refreshIssued, ok := t.Extra(oauth.RefreshTokenIssueTime).(time.Time)
		if !ok || refreshIssued.After(cached.RefreshTokenIssueTime) || cached.RefreshToken == "" {
			cached.RefreshToken = t.RefreshToken
			cached.RefreshTokenIssueTime = refreshIssued
			cached.RefreshTokenExpiry, _ = t.Extra(oauth.RefreshTokenExpiry).(time.Time)
			updatedRefresh = true
		}
		if audience, ok := t.Extra("audience").(string); ok && audience != "" {
			if oldToken, ok := cached.AccessTokens[audience]; !ok || updatedRefresh || t.Expiry.After(oldToken.Expiry) || t.AccessToken == "" {
				cached.AccessTokens[audience] = &accessToken{
					AccessToken: t.AccessToken,
					TokenType:   t.TokenType,
					Expiry:      t.Expiry,
				}
			}
		}

		return json.MarshalIndent(cached, "", "\t")
	})
}
