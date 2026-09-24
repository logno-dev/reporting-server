package storagecontrol

import (
	"context"
	"strings"
	"testing"
)

func TestBuildClientFilesystem(t *testing.T) {
	client, err := buildClient("filesystem", []byte(`{"root":"`+t.TempDir()+`"}`), nil)
	if err != nil {
		t.Fatalf("build filesystem client: %v", err)
	}
	result, err := client.Put(context.Background(), "reports/test.pdf", []byte("pdf"))
	if err != nil {
		t.Fatalf("put with filesystem client: %v", err)
	}
	if result.SHA256 == "" {
		t.Fatal("expected SHA-256 digest")
	}
}

func TestBuildClientRejectsUnknownConfig(t *testing.T) {
	_, err := buildClient("filesystem", []byte(`{"root":"/tmp/reports","extra":true}`), nil)
	if err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("expected unknown field error, got %v", err)
	}
}

func TestBuildClientS3RequiresCompleteSchemaWithoutExposingCredentials(t *testing.T) {
	secret := "do-not-expose-this-secret"
	_, err := buildClient("s3",
		[]byte(`{"endpoint":"s3.example.test","bucket":"reports","region":"auto"}`),
		[]byte(`{"accessKeyId":"key","secretAccessKey":"`+secret+`"}`),
	)
	if err == nil || !strings.Contains(err.Error(), "useSSL") {
		t.Fatalf("expected missing useSSL error, got %v", err)
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatal("validation error exposed credentials")
	}
}

func TestBuildClientRejectsNonObjectConfig(t *testing.T) {
	_, err := buildClient("filesystem", []byte(`null`), nil)
	if err == nil || !strings.Contains(err.Error(), "JSON object") {
		t.Fatalf("expected object validation error, got %v", err)
	}
}

func TestMatchesSHA256(t *testing.T) {
	if !matchesSHA256([]byte("pdf"), "c35b21d6ca39aa7cc3b79a705d989f1a6e88b99ab43988d74048799e3db926a3") {
		t.Fatal("expected matching digest")
	}
	if matchesSHA256([]byte("tampered"), "c35b21d6ca39aa7cc3b79a705d989f1a6e88b99ab43988d74048799e3db926a3") {
		t.Fatal("expected digest mismatch")
	}
}
