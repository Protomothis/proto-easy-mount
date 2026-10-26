package oidcflow

import (
	"errors"
	"fmt"
	"net/http"
	"testing"

	"golang.org/x/oauth2"
)

func TestRejected(t *testing.T) {
	refused := fmt.Errorf("refresh token: %w", &oauth2.RetrieveError{Response: &http.Response{StatusCode: 400}, ErrorCode: "invalid_grant"})
	if !Rejected(refused) {
		t.Fatal("invalid_grant not treated as rejected")
	}
	for _, err := range []error{
		errors.New("dial tcp: no such host"),
		&oauth2.RetrieveError{Response: &http.Response{StatusCode: 503}},
	} {
		if Rejected(err) {
			t.Fatalf("%v treated as rejected; it should be retried", err)
		}
	}
}
