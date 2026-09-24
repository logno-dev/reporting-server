package storage

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"mime"
	"net/url"
	"path/filepath"
	"strings"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

type S3 struct {
	client *minio.Client
	bucket string
}

func (s *S3) Test(ctx context.Context) error {
	exists, err := s.client.BucketExists(ctx, s.bucket)
	if err != nil {
		return fmt.Errorf("check bucket access: %w", err)
	}
	if !exists {
		return fmt.Errorf("bucket %q does not exist or is not accessible", s.bucket)
	}
	probe := make([]byte, 16)
	if _, err := rand.Read(probe); err != nil {
		return fmt.Errorf("create connectivity probe: %w", err)
	}
	key := ".reporting-connectivity/" + hex.EncodeToString(probe)
	if _, err := s.client.PutObject(ctx, s.bucket, key, bytes.NewReader(nil), 0, minio.PutObjectOptions{}); err != nil {
		return fmt.Errorf("test bucket write access: %w", err)
	}
	if err := s.client.RemoveObject(ctx, s.bucket, key, minio.RemoveObjectOptions{}); err != nil {
		return fmt.Errorf("remove connectivity probe %q: %w", key, err)
	}
	return nil
}

func NewS3(endpoint, bucket, region, accessKeyID, secretAccessKey string, useSSL bool) (*S3, error) {
	normalized, secure, err := normalizeEndpoint(endpoint, useSSL)
	if err != nil {
		return nil, err
	}
	client, err := minio.New(normalized, &minio.Options{
		Creds:  credentials.NewStaticV4(accessKeyID, secretAccessKey, ""),
		Secure: secure,
		Region: region,
	})
	if err != nil {
		return nil, fmt.Errorf("configure S3 client: %w", err)
	}
	return &S3{client: client, bucket: bucket}, nil
}

func (s *S3) Put(ctx context.Context, key string, contents []byte) (Result, error) {
	contentType := mime.TypeByExtension(filepath.Ext(key))
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	_, err := s.client.PutObject(ctx, s.bucket, key, bytes.NewReader(contents), int64(len(contents)), minio.PutObjectOptions{ContentType: contentType})
	if err != nil {
		return Result{}, fmt.Errorf("upload artifact %q: %w", key, err)
	}
	digest := sha256.Sum256(contents)
	return Result{Key: key, SHA256: hex.EncodeToString(digest[:])}, nil
}

func (s *S3) Get(ctx context.Context, key string) ([]byte, error) {
	object, err := s.client.GetObject(ctx, s.bucket, key, minio.GetObjectOptions{})
	if err != nil {
		return nil, fmt.Errorf("open artifact %q: %w", key, err)
	}
	defer object.Close()
	contents, err := io.ReadAll(object)
	if err != nil {
		return nil, fmt.Errorf("download artifact %q: %w", key, err)
	}
	return contents, nil
}

func normalizeEndpoint(endpoint string, defaultSecure bool) (string, bool, error) {
	endpoint = strings.TrimSpace(endpoint)
	if !strings.Contains(endpoint, "://") {
		return strings.TrimSuffix(endpoint, "/"), defaultSecure, nil
	}
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return "", false, fmt.Errorf("invalid S3 endpoint %q", endpoint)
	}
	if parsed.Path != "" && parsed.Path != "/" {
		return "", false, fmt.Errorf("S3 endpoint must not contain a path")
	}
	return parsed.Host, parsed.Scheme == "https", nil
}
