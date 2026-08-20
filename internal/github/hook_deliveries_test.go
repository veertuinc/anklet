package github

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	gogithub "github.com/google/go-github/v74/github"
)

func TestListFailedHookDeliveriesQuery(t *testing.T) {
	t.Parallel()
	var gotURL *url.URL
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotURL = r.URL
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`[]`))
	}))
	t.Cleanup(server.Close)

	client := gogithub.NewClient(server.Client())
	base, err := url.Parse(server.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	client.BaseURL = base

	_, _, err = ListFailedHookDeliveries(context.Background(), client, "acme", "", 99, &gogithub.ListCursorOptions{
		PerPage: 100,
		Cursor:  "next-page",
	})
	if err != nil {
		t.Fatalf("ListFailedHookDeliveries: %v", err)
	}
	if gotURL == nil {
		t.Fatal("expected request")
	}
	if gotURL.Path != "/orgs/acme/hooks/99/deliveries" {
		t.Errorf("path = %q", gotURL.Path)
	}
	q := gotURL.Query()
	if q.Get("status") != "failure" {
		t.Errorf("status = %q, want failure", q.Get("status"))
	}
	if q.Get("per_page") != "100" {
		t.Errorf("per_page = %q, want 100", q.Get("per_page"))
	}
	if q.Get("cursor") != "next-page" {
		t.Errorf("cursor = %q, want next-page", q.Get("cursor"))
	}
}

func TestListFailedHookDeliveriesRepoPath(t *testing.T) {
	t.Parallel()
	var gotPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[]`))
	}))
	t.Cleanup(server.Close)

	client := gogithub.NewClient(server.Client())
	base, err := url.Parse(server.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	client.BaseURL = base

	_, _, err = ListFailedHookDeliveries(context.Background(), client, "acme", "widgets", 7, &gogithub.ListCursorOptions{PerPage: 100})
	if err != nil {
		t.Fatalf("ListFailedHookDeliveries: %v", err)
	}
	if gotPath != "/repos/acme/widgets/hooks/7/deliveries" {
		t.Errorf("path = %q", gotPath)
	}
}
