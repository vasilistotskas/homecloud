package icloud

import (
	"context"
	"errors"
	"fmt"
	"net/http"
)

// CPLModifyRecordsRequest is CloudKit's body for private/records/modify.
type CPLModifyRecordsRequest struct {
	Operations []CPLOperation `json:"operations"`
}

// CPLOperation is one operation in a CPLModifyRecordsRequest.
type CPLOperation struct {
	OperationType string    `json:"operationType"`
	Record        CPLRecord `json:"record"`
}

// CPLRecord is one CloudKit record to modify.
type CPLRecord struct {
	RecordName string         `json:"recordName"`
	RecordType string         `json:"recordType"`
	Fields     map[string]any `json:"fields,omitempty"`
}

// CPLFieldValue represents a typed CloudKit field value.
type CPLFieldValue struct {
	Value any `json:"value"`
}

// CPLModifyRecordsResponse is CloudKit's answer to private/records/modify.
type CPLModifyRecordsResponse struct {
	Records []struct {
		RecordName string `json:"recordName"`
		RecordType string `json:"recordType"`
		Fields     map[string]struct {
			Value any `json:"value"`
		} `json:"fields"`
	} `json:"records"`
}

// DeletePhotoAsset marks an asset for deletion in the owner's Cloud Photo Library (CPL)
// by setting isDeleted=1 on its CPLAsset record in the private CloudKit database.
// Apple moves the asset to Recently Deleted (30-day safety net).
func (c *Client) DeletePhotoAsset(ctx context.Context, assetID string) error {
	if assetID == "" {
		return errors.New("delete photo asset: asset id is required")
	}
	svc, err := c.Service(ServiceCKDatabase)
	if err != nil {
		return err
	}
	endpoint := svc.URL + "/database/1/com.apple.photos.cloud/current/private/records/modify"

	req := CPLModifyRecordsRequest{
		Operations: []CPLOperation{
			{
				OperationType: "update",
				Record: CPLRecord{
					RecordName: assetID,
					RecordType: "CPLAsset",
					Fields: map[string]any{
						"isDeleted": CPLFieldValue{Value: 1},
					},
				},
			},
		},
	}

	var resp CPLModifyRecordsResponse
	if err := c.Call(ctx, http.MethodPost, endpoint, req, &resp); err != nil {
		return fmt.Errorf("cpl modify records: %w", err)
	}

	for _, r := range resp.Records {
		if r.RecordName == assetID {
			if v, ok := r.Fields["isDeleted"]; ok && fmt.Sprint(v.Value) == "1" {
				return nil
			}
		}
	}
	return fmt.Errorf("cpl modify records: asset %s deletion unconfirmed by cloudkit", assetID)
}
