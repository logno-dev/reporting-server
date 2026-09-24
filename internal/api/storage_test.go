package api

import (
	"encoding/json"
	"testing"
)

func TestProfileInputAcceptsStrictS3Profile(t *testing.T) {
	rate, concurrency := 10, 2
	input, err := profileInput(profileRequest{
		Name: "Archive S3", BackendType: "s3",
		PublicConfig:     json.RawMessage(`{"endpoint":"s3.example.test","bucket":"reports","region":"auto","useSSL":true}`),
		Credentials:      json.RawMessage(`{"accessKeyId":"key","secretAccessKey":"secret"}`),
		RequestRateLimit: &rate, TransferConcurrency: &concurrency,
	})
	if err != nil {
		t.Fatalf("profileInput returned error: %v", err)
	}
	if input.State != "active" || input.BackendType != "s3" {
		t.Fatalf("unexpected input: %#v", input)
	}
}

func TestProfileInputRejectsFilesystemAndInvalidLimits(t *testing.T) {
	invalid := 0
	tests := []profileRequest{
		{Name: "Other root", BackendType: "filesystem"},
		{Name: "Archive S3", BackendType: "s3", TransferConcurrency: &invalid},
		{Name: "x", BackendType: "s3"},
	}
	for _, test := range tests {
		if _, err := profileInput(test); err == nil {
			t.Fatalf("expected rejection for %#v", test)
		}
	}
}
