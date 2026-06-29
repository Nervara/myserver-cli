package main

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestExportTenantWithDataSendsS3StorageID(t *testing.T) {
	var gotQuery string
	api := &apiClient{
		url: "https://example.invalid",
		hc: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			gotQuery = r.URL.RawQuery
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader(`{"filename":"tenant.mbak","projects":1,"artifacts":0,"exported_at":"t","backup":"blob"}`)),
				Header:     make(http.Header),
			}, nil
		})},
	}

	if _, err := api.exportTenant(true, 77, ""); err != nil {
		t.Fatalf("exportTenant: %v", err)
	}
	if gotQuery != "include_data=true&s3_storage_id=77" {
		t.Fatalf("query = %q, want include_data=true&s3_storage_id=77", gotQuery)
	}
}

func TestExportTenantSendsRecoveryKeyHeader(t *testing.T) {
	var gotHeader string
	api := &apiClient{
		url: "https://example.invalid",
		hc: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			gotHeader = r.Header.Get("X-Tenant-Backup-Recovery-Key")
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader(`{"filename":"tenant.mbak","projects":1,"artifacts":0,"exported_at":"t","backup":"blob"}`)),
				Header:     make(http.Header),
			}, nil
		})},
	}

	if _, err := api.exportTenant(false, 0, "portable-key"); err != nil {
		t.Fatalf("exportTenant: %v", err)
	}
	if gotHeader != "portable-key" {
		t.Fatalf("recovery header = %q, want portable-key", gotHeader)
	}
}

func TestImportTenantSendsRecoveryKey(t *testing.T) {
	var gotBody string
	api := &apiClient{
		url: "https://example.invalid",
		hc: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			body, _ := io.ReadAll(r.Body)
			gotBody = string(body)
			return &http.Response{
				StatusCode: http.StatusCreated,
				Body:       io.NopCloser(strings.NewReader(`{"projects_created":1}`)),
				Header:     make(http.Header),
			}, nil
		})},
	}

	if _, err := api.importTenant("blob", false, nil, nil, "portable-key"); err != nil {
		t.Fatalf("importTenant: %v", err)
	}
	if !strings.Contains(gotBody, `"recovery_key":"portable-key"`) {
		t.Fatalf("body missing recovery key: %s", gotBody)
	}
}

func TestImportTenantSendsTargetServerID(t *testing.T) {
	var gotBody string
	api := &apiClient{
		url: "https://example.invalid",
		hc: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			body, _ := io.ReadAll(r.Body)
			gotBody = string(body)
			return &http.Response{
				StatusCode: http.StatusCreated,
				Body:       io.NopCloser(strings.NewReader(`{"projects_created":1}`)),
				Header:     make(http.Header),
			}, nil
		})},
	}

	targetID := int64(42)
	if _, err := api.importTenant("blob", false, map[string]int64{"8": 9}, &targetID, ""); err != nil {
		t.Fatalf("importTenant: %v", err)
	}
	if !strings.Contains(gotBody, `"target_server_id":42`) {
		t.Fatalf("body missing target server id: %s", gotBody)
	}
	if strings.Contains(gotBody, "server_remap") {
		t.Fatalf("target server path should not send server_remap: %s", gotBody)
	}
}
