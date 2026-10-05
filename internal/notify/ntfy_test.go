package notify

import (
	"context"
	"encoding/json/v2"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/vasilistotskas/homecloud/internal/config"
)

func TestSend(t *testing.T) {
	var got publishRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/" || r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("unexpected request %s %s %q", r.Method, r.URL.Path, r.Header.Get("Content-Type"))
		}
		body, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(body, &got); err != nil {
			t.Errorf("decode body: %v", err)
		}
		_, _ = w.Write([]byte(`{"id":"x","event":"message"}`))
	}))
	defer srv.Close()

	n := New(config.Notify{Server: srv.URL, Topic: "homecloud_topic_for_tests_123"})
	err := n.Send(context.Background(), Msg{Title: "Αντίγραφο", Body: "ok", Priority: PriorityHigh, Tags: []string{"floppy_disk"}})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	want := publishRequest{Topic: "homecloud_topic_for_tests_123", Title: "Αντίγραφο", Message: "ok", Priority: PriorityHigh, Tags: []string{"floppy_disk"}}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("request mismatch (-want +got):\n%s", diff)
	}
}

func TestSendNon200(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, `{"code":42901,"error":"limit reached"}`, http.StatusTooManyRequests)
	}))
	defer srv.Close()

	err := New(config.Notify{Server: srv.URL, Topic: "t"}).Send(context.Background(), Msg{Body: "x"})
	if err == nil || !strings.Contains(err.Error(), "HTTP 429") {
		t.Fatalf("want HTTP 429 error, got %v", err)
	}
}
