package oidcflow

import (
	"net/http/httptest"
	"testing"
)

func TestCallbackCode(t *testing.T) {
	cases := []struct {
		query   string
		code    string
		wantErr bool
	}{
		{"?state=s&code=c", "c", false},
		{"?state=other&code=c", "", true},
		{"?state=s", "", true},
		{"?error=access_denied&error_description=nope&state=s", "", true},
	}
	for _, c := range cases {
		code, err := callbackCode(httptest.NewRequest("GET", "/callback"+c.query, nil), "s")
		if (err != nil) != c.wantErr || code != c.code {
			t.Errorf("%s: code=%q err=%v", c.query, code, err)
		}
	}
}
