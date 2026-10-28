package invite_test

import (
	"testing"

	"github.com/protomothis/proto-easy-mount/internal/invite"
	"github.com/protomothis/proto-easy-mount/internal/provider"
	_ "github.com/protomothis/proto-easy-mount/internal/provider/opencloud"
	_ "github.com/protomothis/proto-easy-mount/internal/provider/webdav"
)

func get(t *testing.T, name string) provider.Provider {
	t.Helper()
	p, err := provider.Get(name)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// A link carries the server and its client ID, and reads back the same.
func TestRoundTrip(t *testing.T) {
	oc := get(t, "opencloud")
	acct := provider.Account{Provider: "opencloud", Server: "https://drive.example.com", User: "kim",
		Extra: map[string]string{"client_id": "396758001312088073", "issuer": "https://auth.example.com"}}
	l := invite.For(oc, acct)
	s := l.String()
	if s != "proto-easy-mount://add?client_id=396758001312088073&provider=opencloud&server=https%3A%2F%2Fdrive.example.com" {
		t.Fatalf("link = %s", s)
	}
	if !invite.IsLink(s) {
		t.Fatal("IsLink = false")
	}
	got, err := invite.Parse(s)
	if err != nil {
		t.Fatal(err)
	}
	got = got.Keep(oc)
	if got.Provider != "opencloud" || got.Server != acct.Server || len(got.Options) != 1 || got.Options["client_id"] != "396758001312088073" {
		t.Fatalf("parsed = %+v", got)
	}
}

// Who signs in never goes into a link, and a crafted link cannot fill it in.
func TestNoPersonalOptions(t *testing.T) {
	dav := get(t, "webdav")
	acct := provider.Account{Provider: "webdav", Server: "https://nas.example.com/dav", User: "kim",
		Extra: map[string]string{"user": "kim", "password": "hunter2"}}
	if l := invite.For(dav, acct); len(l.Options) != 0 {
		t.Fatalf("webdav link carries %v", l.Options)
	}
	l, err := invite.Parse("proto-easy-mount://add?provider=webdav&server=https%3A%2F%2Fnas.example.com%2Fdav&user=evil&password=x")
	if err != nil {
		t.Fatal(err)
	}
	if kept := l.Keep(dav); len(kept.Options) != 0 {
		t.Fatalf("kept %v", kept.Options)
	}
}

func TestParseRejects(t *testing.T) {
	for _, raw := range []string{
		"https://drive.example.com",
		"proto-easy-mount://remove?provider=opencloud&server=https%3A%2F%2Fa.example",
		"proto-easy-mount://add?server=https%3A%2F%2Fa.example",
		"proto-easy-mount://add?provider=opencloud",
		"proto-easy-mount://add?provider=opencloud&server=file%3A%2F%2F%2FC%3A%2F",
		"proto-easy-mount://add?provider=opencloud&server=javascript%3Aalert(1)",
	} {
		if l, err := invite.Parse(raw); err == nil {
			t.Errorf("Parse(%q) = %+v, want an error", raw, l)
		}
	}
	// the opaque form some apps produce is fine
	if _, err := invite.Parse("proto-easy-mount:add?provider=opencloud&server=https%3A%2F%2Fa.example"); err != nil {
		t.Errorf("opaque form: %v", err)
	}
}
