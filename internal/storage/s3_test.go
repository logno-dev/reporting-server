package storage

import "testing"

func TestNormalizeEndpoint(t *testing.T) {
	tests := []struct {
		input         string
		defaultSecure bool
		wantEndpoint  string
		wantSecure    bool
	}{
		{"account.r2.cloudflarestorage.com", true, "account.r2.cloudflarestorage.com", true},
		{"https://account.r2.cloudflarestorage.com", false, "account.r2.cloudflarestorage.com", true},
		{"http://minio:9000", true, "minio:9000", false},
	}
	for _, test := range tests {
		endpoint, secure, err := normalizeEndpoint(test.input, test.defaultSecure)
		if err != nil {
			t.Fatalf("normalizeEndpoint(%q) error = %v", test.input, err)
		}
		if endpoint != test.wantEndpoint || secure != test.wantSecure {
			t.Errorf("normalizeEndpoint(%q) = (%q, %t), want (%q, %t)", test.input, endpoint, secure, test.wantEndpoint, test.wantSecure)
		}
	}
}

func TestNormalizeEndpointRejectsPath(t *testing.T) {
	if _, _, err := normalizeEndpoint("https://storage.example.com/path", true); err == nil {
		t.Fatal("normalizeEndpoint() accepted an endpoint path")
	}
}
