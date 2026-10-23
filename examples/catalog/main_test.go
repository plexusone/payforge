package main

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestWithoutAKeyTheSpecIsCheckedAndNothingElseHappens(t *testing.T) {
	var out bytes.Buffer
	if err := run(context.Background(), "pricing.json", "", false, "", &out); err != nil {
		t.Fatal(err)
	}
	if got := out.String(); !strings.Contains(got, "taskboard: 4 plans") || !strings.Contains(got, "free mode") {
		t.Fatalf("output = %q", got)
	}
}

func TestAMissingOrInvalidSpecIsAnError(t *testing.T) {
	if err := run(context.Background(), "nope.json", "", false, "", &bytes.Buffer{}); err == nil {
		t.Error("a missing file was accepted")
	}
	if err := run(context.Background(), "main.go", "", false, "", &bytes.Buffer{}); err == nil {
		t.Error("a file that is not a spec was accepted")
	}
}

// An empty Stripe account: no product exists and no price is found.
func emptyStripe(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/v1/products/"):
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":{"type":"invalid_request_error","code":"resource_missing","message":"No such product"}}`))
		case r.Method == http.MethodGet && r.URL.Path == "/v1/prices":
			_, _ = w.Write([]byte(`{"object":"list","url":"/v1/prices","has_more":false,"data":[]}`))
		default:
			t.Errorf("a dry run made the request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestADryRunShowsWhatWouldBeCreatedAndWritesNothing(t *testing.T) {
	srv := emptyStripe(t)
	var out bytes.Buffer
	if err := run(context.Background(), "pricing.json", "sk_test_fake_key", false, srv.URL, &out); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	for _, want := range []string{"create_product", "create_price", "taskboard/plus/month", "dry run: nothing was written"} {
		if !strings.Contains(got, want) {
			t.Errorf("output lacks %q:\n%s", want, got)
		}
	}
}
