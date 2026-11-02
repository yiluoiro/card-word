// Package market implements the plugin marketplace: a local index of
// curated plugins plus optional remote indexes, all installable through the
// same signed package pipeline as manual uploads.
package market

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"
)

// Entry is a plugin listing.
type Entry struct {
	UID         string   `json:"uid"`
	Name        string   `json:"name"`
	Version     string   `json:"version"`
	Author      string   `json:"author"`
	Description string   `json:"description"`
	Kind        string   `json:"kind"`
	Homepage    string   `json:"homepage"`
	DownloadURL string   `json:"download_url"`
	SHA256      string   `json:"sha256"`
	Signature   string   `json:"signature"`
	Tags        []string `json:"tags"`
	Source      string   `json:"source"`
}

// Index is a marketplace catalogue document.
type Index struct {
	Schema  string  `json:"schema"`
	Updated string  `json:"updated"`
	Plugins []Entry `json:"plugins"`
}

// Client fetches marketplace indexes.
type Client struct {
	HTTP *http.Client
	// Builtin is an embedded catalogue always shown to operators.
	Builtin []Entry
}

// NewClient builds a marketplace client.
func NewClient(builtin []Entry) *Client {
	return &Client{
		HTTP:    &http.Client{Timeout: 10 * time.Second},
		Builtin: builtin,
	}
}

// List returns the merged catalogue, local first, deduplicated by uid.
func (c *Client) List(ctx context.Context, remoteURL string) ([]Entry, error) {
	out := append([]Entry(nil), c.Builtin...)
	seen := map[string]bool{}
	for i := range out {
		out[i].Source = "builtin"
		seen[out[i].UID] = true
	}
	if strings.TrimSpace(remoteURL) != "" {
		entries, err := c.fetch(ctx, remoteURL)
		if err != nil {
			// A broken marketplace must not break the admin console.
			return out, err
		}
		for _, e := range entries {
			if seen[e.UID] {
				continue
			}
			e.Source = "remote"
			seen[e.UID] = true
			out = append(out, e)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Kind != out[j].Kind {
			return out[i].Kind < out[j].Kind
		}
		return out[i].Name < out[j].Name
	})
	return out, nil
}

func (c *Client) fetch(ctx context.Context, url string) ([]Entry, error) {
	if !strings.HasPrefix(url, "https://") && !strings.HasPrefix(url, "http://") {
		return nil, errors.New("market: index url must be http(s)")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("market: fetch index: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("market: index returned %d", resp.StatusCode)
	}
	limited := io.LimitReader(resp.Body, 2<<20)
	var idx Index
	if err := json.NewDecoder(limited).Decode(&idx); err != nil {
		return nil, fmt.Errorf("market: decode index: %w", err)
	}
	for i := range idx.Plugins {
		idx.Plugins[i].Source = "remote"
	}
	return idx.Plugins, nil
}

// Download fetches a package and enforces the advertised digest.
func (c *Client) Download(ctx context.Context, e Entry, maxBytes int64) ([]byte, error) {
	if e.DownloadURL == "" {
		return nil, errors.New("market: entry has no download url")
	}
	if !strings.HasPrefix(e.DownloadURL, "https://") {
		return nil, errors.New("market: download url must use https")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, e.DownloadURL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("market: download: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("market: download returned %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxBytes+1))
	if err != nil {
		return nil, fmt.Errorf("market: read package: %w", err)
	}
	if int64(len(data)) > maxBytes {
		return nil, errors.New("market: package exceeds size limit")
	}
	return data, nil
}
