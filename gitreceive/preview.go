package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
)

type previewRemoteInfo struct {
	Available bool     `json:"available"`
	Name      string   `json:"name,omitempty"`
	URL       string   `json:"url,omitempty"`
	Pipeline  string   `json:"pipeline,omitempty"`
	Reason    string   `json:"reason,omitempty"`
	AppIDs    []string `json:"app_ids,omitempty"`
	AppNames  []string `json:"app_names,omitempty"`
	// Branches are the preview branches that still have an app. A missing
	// field means an older pipeline plugin; an empty list means every
	// preview app is gone and leftover branch refs should be deleted.
	Branches []string `json:"branches"`
}

type previewEnsureResult struct {
	AppID   string `json:"app_id"`
	AppName string `json:"app_name"`
}

type previewLookup interface {
	PreviewRemote(pipeline string) (*previewRemoteInfo, error)
	EnsurePreview(pipeline, branch, sha string) (*previewEnsureResult, error)
}

type pipelinePreviewClient struct {
	base       string
	key        string
	httpClient *http.Client
}

func newPipelinePreviewClient() *pipelinePreviewClient {
	base := strings.TrimRight(strings.TrimSpace(os.Getenv("PIPELINE_URL")), "/")
	if base == "" {
		base = "http://pipeline.discoverd"
	}
	return &pipelinePreviewClient{
		base:       base,
		key:        os.Getenv("CONTROLLER_KEY"),
		httpClient: http.DefaultClient,
	}
}

func (c *pipelinePreviewClient) PreviewRemote(pipeline string) (*previewRemoteInfo, error) {
	var out previewRemoteInfo
	status, err := c.do(http.MethodGet, "/pipelines/"+url.PathEscape(pipeline)+"/preview-remote", nil, &out)
	if err != nil {
		if status == http.StatusNotFound {
			return &previewRemoteInfo{Available: false, Reason: err.Error()}, nil
		}
		return nil, err
	}
	return &out, nil
}

func (c *pipelinePreviewClient) EnsurePreview(pipeline, branch, sha string) (*previewEnsureResult, error) {
	var out previewEnsureResult
	_, err := c.do(http.MethodPost, "/pipelines/"+url.PathEscape(pipeline)+"/previews", map[string]string{
		"branch": branch,
		"sha":    sha,
		"source": "git",
	}, &out)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(out.AppName) == "" {
		out.AppName = out.AppID
	}
	if strings.TrimSpace(out.AppName) == "" {
		return nil, fmt.Errorf("pipeline plugin did not return a preview app name")
	}
	return &out, nil
}

func (c *pipelinePreviewClient) do(method, path string, body any, out any) (int, error) {
	if c == nil || c.base == "" {
		return 0, fmt.Errorf("preview remotes require the pipelines plugin")
	}
	var rdr io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return 0, err
		}
		rdr = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, c.base+path, rdr)
	if err != nil {
		return 0, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.key != "" {
		req.SetBasicAuth("", c.key)
	}
	client := c.httpClient
	if client == nil {
		client = http.DefaultClient
	}
	res, err := client.Do(req)
	if err != nil {
		return 0, fmt.Errorf("pipelines plugin is not available: %w", err)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	if res.StatusCode >= 300 {
		msg := strings.TrimSpace(string(raw))
		var parsed struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(raw, &parsed) == nil && parsed.Error != "" {
			msg = parsed.Error
		}
		if msg == "" {
			msg = res.Status
		}
		return res.StatusCode, fmt.Errorf("%s", msg)
	}
	if out == nil || len(raw) == 0 {
		return res.StatusCode, nil
	}
	return res.StatusCode, json.Unmarshal(raw, out)
}

func runEnsurePreview(args []string) error {
	if len(args) < 2 {
		return fmt.Errorf("usage: gitreceived -ensure-preview <pipeline> <branch> [sha]")
	}
	pipeline, branch := args[0], args[1]
	sha := ""
	if len(args) > 2 {
		sha = args[2]
	}
	out, err := newPipelinePreviewClient().EnsurePreview(pipeline, branch, sha)
	if err != nil {
		return err
	}
	fmt.Println(out.AppName)
	return nil
}

var previewPrereceiveHook = []byte(`#!/bin/bash
set -eo pipefail;

` + archiveTreeFunc + `

while read oldrev newrev refname; do
	if [[ $refname != refs/heads/* ]]; then
		echo "Ignored push to $refname; push a branch to create a preview app."
		continue
	fi
	branch="${refname#refs/heads/}"
	if [[ $newrev =~ ^0+$ ]]; then
		echo "Ignored delete of $branch."
		continue
	fi
	echo "-----> Creating preview app for $branch..."
	app=$(/bin/gitreceived -ensure-preview "$RECEIVE_PIPELINE" "$branch" "$newrev")
	echo "-----> Deploying $branch to $app..."
	git-archive-all $newrev | stdbuf -oL -eL /bin/flynn-receiver "$app" "$newrev" --meta git=true --meta "git.commit=$newrev" --meta "git.branch=$branch" --meta flynn-preview=true | sed -u "s/^/"$'\e[1G\e[K'"/"
done
`)
