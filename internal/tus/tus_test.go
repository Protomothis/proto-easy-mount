package tus_test

import (
	"bytes"
	"context"
	"errors"
	"net/http/httptest"
	"testing"

	"github.com/protomothis/proto-easy-mount/internal/tus"
	"github.com/protomothis/proto-easy-mount/internal/tus/tustest"
)

func TestCreatePatchResume(t *testing.T) {
	fake := &tustest.FakeServer{}
	srv := httptest.NewServer(fake)
	defer srv.Close()
	c := &tus.Client{Token: func(context.Context) (string, error) { return "t", nil }}
	ctx := context.Background()

	up, err := c.Create(ctx, srv.URL+"/dav/spaces/x/folder/", "가 b.bin", 10, map[string]string{"mtime": "1"})
	if err != nil {
		t.Fatal(err)
	}
	off, err := c.Patch(ctx, up, 0, bytes.NewReader([]byte("01234")), 5)
	if err != nil || off != 5 {
		t.Fatalf("patch: %d %v", off, err)
	}
	if off, err := c.Offset(ctx, up); err != nil || off != 5 {
		t.Fatalf("offset: %d %v", off, err)
	}
	// a PATCH at the wrong offset is refused
	var se *tus.StatusError
	if _, err := c.Patch(ctx, up, 0, bytes.NewReader([]byte("x")), 1); !errors.As(err, &se) || se.Status != 409 {
		t.Fatalf("wrong offset accepted: %v", err)
	}
	if off, err := c.Patch(ctx, up, 5, bytes.NewReader([]byte("56789")), 5); err != nil || off != 10 {
		t.Fatalf("final patch: %d %v", off, err)
	}
	u := fake.Uploads()["/dav/spaces/x/folder/가 b.bin"]
	if u == nil || !u.Done() || string(u.Data) != "0123456789" || u.Meta["mtime"] != "1" {
		t.Fatalf("upload = %+v", u)
	}
}
