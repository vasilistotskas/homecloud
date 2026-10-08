package icloud

import (
	"context"
	"encoding/json/v2"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestDeletePhotoAsset(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/database/1/com.apple.photos.cloud/current/private/records/modify", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "bad method", http.StatusMethodNotAllowed)
			return
		}
		body, _ := io.ReadAll(r.Body)
		var req CPLModifyRecordsRequest
		if err := json.Unmarshal(body, &req); err != nil {
			http.Error(w, "bad json", http.StatusBadRequest)
			return
		}
		if len(req.Operations) != 1 || req.Operations[0].Record.RecordName != "asset-123" {
			http.Error(w, "unexpected record", http.StatusBadRequest)
			return
		}
		resp := CPLModifyRecordsResponse{
			Records: []struct {
				RecordName string `json:"recordName"`
				RecordType string `json:"recordType"`
				Fields     map[string]struct {
					Value any `json:"value"`
				} `json:"fields"`
			}{
				{
					RecordName: "asset-123",
					RecordType: "CPLAsset",
					Fields: map[string]struct {
						Value any `json:"value"`
					}{
						"isDeleted": {Value: 1},
					},
				},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		data, _ := json.Marshal(resp)
		_, _ = w.Write(data)
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	session := Session{
		Services: map[string]Service{
			ServiceCKDatabase: {URL: srv.URL},
		},
	}
	c, err := New(Endpoints{Auth: srv.URL, Setup: srv.URL, Home: srv.URL}, session, time.Minute, time.Now)
	if err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	if err := c.DeletePhotoAsset(ctx, ""); err == nil {
		t.Fatal("empty asset ID passed; want error")
	}

	if err := c.DeletePhotoAsset(ctx, "asset-123"); err != nil {
		t.Fatalf("DeletePhotoAsset failed: %v", err)
	}
}

func TestDeletePhotoAssetMissingService(t *testing.T) {
	c, err := New(AppleEndpoints, Session{}, time.Minute, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	err = c.DeletePhotoAsset(context.Background(), "asset-1")
	if !errors.Is(err, ErrSignInRequired) {
		t.Fatalf("DeletePhotoAsset with empty session = %v, want ErrSignInRequired", err)
	}
}
