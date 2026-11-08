// Copyright 2026 PgVillage
// SPDX-License-Identifier: GPL-3.0-or-later

// Package github is a small client for the parts of the GitHub REST API that ghsettings uses. Rulesets and
// repository settings are kept as raw JSON maps, so every field of the API is available.
package github

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// DefaultBaseURL is the base URL of the GitHub REST API.
const DefaultBaseURL = "https://api.github.com"

// ErrNotFound is returned for a 404 response.
var ErrNotFound = errors.New("not found")

const (
	apiVersion   = "2022-11-28"
	perPage      = 100
	maxErrorBody = 1024
)

// Client calls the GitHub REST API with a token.
type Client struct {
	BaseURL string
	Token   string
	HTTP    *http.Client
}

// NewClient returns a client for api.github.com.
func NewClient(token string) *Client {
	return &Client{BaseURL: DefaultBaseURL, Token: token, HTTP: &http.Client{Timeout: time.Minute}}
}

// do sends a request and decodes the JSON response into out (when not nil). It returns the next page URL from the
// Link header, if any.
func (c *Client) do(ctx context.Context, method, target string, body io.Reader, out any) (string, error) {
	if !strings.HasPrefix(target, "http") {
		target = strings.TrimSuffix(c.BaseURL, "/") + "/" + strings.TrimPrefix(target, "/")
	}
	req, err := http.NewRequestWithContext(ctx, method, target, body)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", apiVersion)
	req.Header.Set("User-Agent", "ghsettings")
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusNotFound {
		return "", fmt.Errorf("%s %s: %w", method, req.URL.Path, ErrNotFound)
	}
	if resp.StatusCode >= http.StatusBadRequest {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBody))
		return "", fmt.Errorf("%s %s: %s: %s", method, req.URL.Path, resp.Status, strings.TrimSpace(string(msg)))
	}
	if out != nil {
		if err = json.NewDecoder(resp.Body).Decode(out); err != nil {
			return "", fmt.Errorf("%s %s: decoding response: %w", method, req.URL.Path, err)
		}
	}
	return nextLink(resp.Header.Get("Link")), nil
}

var linkNext = regexp.MustCompile(`<([^>]+)>;\s*rel="next"`)

func nextLink(header string) string {
	if m := linkNext.FindStringSubmatch(header); m != nil {
		return m[1]
	}
	return ""
}

// get decodes one response.
func (c *Client) get(ctx context.Context, path string, out any) error {
	_, err := c.do(ctx, http.MethodGet, path, nil, out)
	return err
}

// getAll follows the pagination of a list endpoint and returns all items.
func getAll[T any](ctx context.Context, c *Client, path string) ([]T, error) {
	sep := "?"
	if strings.Contains(path, "?") {
		sep = "&"
	}
	next := fmt.Sprintf("%s%sper_page=%d", path, sep, perPage)
	var all []T
	for next != "" {
		var page []T
		var err error
		if next, err = c.do(ctx, http.MethodGet, next, nil, &page); err != nil {
			return nil, err
		}
		all = append(all, page...)
	}
	return all, nil
}

// RepoSummary is a repository in the list of an org.
type RepoSummary struct {
	Name       string `json:"name"`
	Archived   bool   `json:"archived"`
	Fork       bool   `json:"fork"`
	Visibility string `json:"visibility"`
}

// ListRepos returns all repositories of an org.
func (c *Client) ListRepos(ctx context.Context, org string) ([]RepoSummary, error) {
	return getAll[RepoSummary](ctx, c, fmt.Sprintf("orgs/%s/repos?type=all", url.PathEscape(org)))
}

// GetRepo returns all fields of a repository.
func (c *Client) GetRepo(ctx context.Context, owner, repo string) (map[string]any, error) {
	var out map[string]any
	err := c.get(ctx, fmt.Sprintf("repos/%s/%s", url.PathEscape(owner), url.PathEscape(repo)), &out)
	return out, err
}

// RulesetSummary is a ruleset in the list of a repository.
type RulesetSummary struct {
	ID         int64  `json:"id"`
	Name       string `json:"name"`
	SourceType string `json:"source_type"`
}

// FindRuleset returns the ruleset with this name that is defined on the repository itself (not inherited from the
// org), or nil when there is none.
func (c *Client) FindRuleset(ctx context.Context, owner, repo, name string) (map[string]any, error) {
	list, err := getAll[RulesetSummary](ctx, c,
		fmt.Sprintf("repos/%s/%s/rulesets?includes_parents=false", url.PathEscape(owner), url.PathEscape(repo)))
	if err != nil {
		return nil, err
	}
	for _, rs := range list {
		if rs.Name != name || (rs.SourceType != "" && rs.SourceType != "Repository") {
			continue
		}
		var out map[string]any
		err = c.get(ctx, fmt.Sprintf("repos/%s/%s/rulesets/%d", url.PathEscape(owner), url.PathEscape(repo), rs.ID),
			&out)
		return out, err
	}
	return nil, nil
}

type content struct {
	Type     string `json:"type"`
	Path     string `json:"path"`
	Content  string `json:"content"`
	Encoding string `json:"encoding"`
}

// ListWorkflows returns the workflow files in .github/workflows of the default branch, by path. A repository without
// workflows returns an empty map.
func (c *Client) ListWorkflows(ctx context.Context, owner, repo string) (map[string][]byte, error) {
	base := fmt.Sprintf("repos/%s/%s/contents/", url.PathEscape(owner), url.PathEscape(repo))
	var entries []content
	err := c.get(ctx, base+".github/workflows", &entries)
	if errors.Is(err, ErrNotFound) {
		return map[string][]byte{}, nil
	}
	if err != nil {
		return nil, err
	}
	files := map[string][]byte{}
	for _, e := range entries {
		if e.Type != "file" || !(strings.HasSuffix(e.Path, ".yml") || strings.HasSuffix(e.Path, ".yaml")) {
			continue
		}
		var file content
		if err = c.get(ctx, base+e.Path, &file); err != nil {
			return nil, err
		}
		if file.Encoding != "base64" {
			return nil, fmt.Errorf("%s/%s: %s: unexpected encoding %q", owner, repo, e.Path, file.Encoding)
		}
		data, decodeErr := base64.StdEncoding.DecodeString(strings.ReplaceAll(file.Content, "\n", ""))
		if decodeErr != nil {
			return nil, fmt.Errorf("%s/%s: %s: %w", owner, repo, e.Path, decodeErr)
		}
		files[e.Path] = data
	}
	return files, nil
}
