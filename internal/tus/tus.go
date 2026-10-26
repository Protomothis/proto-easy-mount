// Package tus is a small client for the tus resumable upload protocol 1.0
// (https://tus.io/protocols/resumable-upload), as OpenCloud and ownCloud
// Infinite Scale speak it: creation plus sequential PATCH requests.
package tus

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
)

const version = "1.0.0"

// Client issues tus requests with a bearer token.
type Client struct {
	HTTP  *http.Client
	Token func(ctx context.Context) (string, error)
}

func (c *Client) do(ctx context.Context, method, target string, body io.Reader, n int64, hdr map[string]string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, target, body)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.ContentLength = n
	}
	req.Header.Set("Tus-Resumable", version)
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	if c.Token != nil {
		tok, err := c.Token(ctx)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	hc := c.HTTP
	if hc == nil {
		hc = http.DefaultClient
	}
	return hc.Do(req)
}

// StatusError is a response the protocol didn't expect.
type StatusError struct {
	Op     string
	Status int
	Body   string
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("tus %s: HTTP %d %s", e.Op, e.Status, strings.TrimSpace(e.Body))
}

func statusErr(op string, resp *http.Response) error {
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
	return &StatusError{Op: op, Status: resp.StatusCode, Body: string(b)}
}

// Create starts an upload of size bytes named name inside the collection
// (a WebDAV folder URL) and returns the URL to PATCH.
func (c *Client) Create(ctx context.Context, collection, name string, size int64, meta map[string]string) (string, error) {
	all := map[string]string{"filename": name}
	for k, v := range meta {
		all[k] = v
	}
	resp, err := c.do(ctx, http.MethodPost, collection, nil, 0, map[string]string{
		"Upload-Length":   strconv.FormatInt(size, 10),
		"Upload-Metadata": encodeMetadata(all),
	})
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		return "", statusErr("create", resp)
	}
	loc := resp.Header.Get("Location")
	if loc == "" {
		return "", errors.New("tus create: no Location header")
	}
	// Location may be relative to the collection.
	base, err := url.Parse(collection)
	if err != nil {
		return "", err
	}
	u, err := base.Parse(loc)
	if err != nil {
		return "", err
	}
	return u.String(), nil
}

// Patch sends n bytes from body starting at offset and returns the
// offset the server now has.
func (c *Client) Patch(ctx context.Context, upload string, offset int64, body io.Reader, n int64) (int64, error) {
	resp, err := c.do(ctx, http.MethodPatch, upload, body, n, map[string]string{
		"Upload-Offset": strconv.FormatInt(offset, 10),
		"Content-Type":  "application/offset+octet-stream",
	})
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		return 0, statusErr("patch", resp)
	}
	return parseOffset(resp)
}

// Offset asks the server how much of the upload it has, for resuming.
func (c *Client) Offset(ctx context.Context, upload string) (int64, error) {
	resp, err := c.do(ctx, http.MethodHead, upload, nil, 0, nil)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent {
		return 0, statusErr("head", resp)
	}
	return parseOffset(resp)
}

func parseOffset(resp *http.Response) (int64, error) {
	v := resp.Header.Get("Upload-Offset")
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("tus: bad Upload-Offset %q", v)
	}
	return n, nil
}

// encodeMetadata renders "key base64(value),key base64(value)" in a
// stable order.
func encodeMetadata(m map[string]string) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, len(keys))
	for i, k := range keys {
		parts[i] = k + " " + base64.StdEncoding.EncodeToString([]byte(m[k]))
	}
	return strings.Join(parts, ",")
}
