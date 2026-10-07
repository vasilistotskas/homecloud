package icloud

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestTransportErrorsNameNoURL fails the connection under a call and a
// download whose URLs carry tokens: net/http's *url.Error would print them.
func TestTransportErrorsNameNoURL(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		conn, _, err := http.NewResponseController(w).Hijack()
		if err != nil {
			t.Error(err)
			return
		}
		_ = conn.Close()
	}))
	t.Cleanup(srv.Close)
	c, err := New(Endpoints{Auth: srv.URL, Setup: srv.URL, Home: srv.URL}, Session{}, time.Minute, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	const secret = "s3cr3t-signature"
	ctx := context.Background()
	var out map[string]any
	callErr := c.Call(ctx, http.MethodGet, srv.URL+"/ws/zone/download/by_id?document_id=doc&token="+secret, nil, &out)
	_, fetchErr := c.Fetch(ctx, srv.URL+"/B/"+secret+"/file?o="+secret, time.Minute)
	for name, err := range map[string]error{"call": callErr, "download": fetchErr} {
		if err == nil {
			t.Fatalf("%s: no error from a dropped connection", name)
		}
		if msg := err.Error(); strings.Contains(msg, secret) || strings.Contains(msg, "?") {
			t.Errorf("%s error names its URL: %s", name, msg)
		}
	}
}

func TestBadContentURLNamesNoURL(t *testing.T) {
	c, err := New(AppleEndpoints, Session{}, time.Minute, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	const secret = "s3cr3t-signature"
	_, err = c.Fetch(context.Background(), "https://content.example/\x7f"+secret, time.Minute)
	if err == nil || strings.Contains(err.Error(), secret) {
		t.Fatalf("bad URL: %v", err)
	}
}
