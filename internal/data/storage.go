package data

import (
	"bytes"
	"context"
	"io"
	"os"

	"github.com/go-kratos/kratos/v2/log"
	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
	"github.com/tx7do/kratos-bootstrap/bootstrap"
)

// StorageClient wraps an S3/RustFS bucket for ticket attachments. It is
// nil when S3 is not configured (TICKET_S3_ENDPOINT unset) — callers must
// nil-check; attachments are then skipped.
type StorageClient struct {
	client *minio.Client
	bucket string
	log    *log.Helper
}

func NewStorageClient(ctx *bootstrap.Context) (*StorageClient, func(), error) {
	l := ctx.NewLoggerHelper("storage/data/ticket-service")

	endpoint := os.Getenv("TICKET_S3_ENDPOINT")
	if endpoint == "" {
		l.Warn("TICKET_S3_ENDPOINT not set — attachment storage disabled")
		return nil, func() {}, nil
	}

	bucket := getEnvOrDefault("TICKET_S3_BUCKET", "ticket")
	region := getEnvOrDefault("TICKET_S3_REGION", "us-east-1")
	client, err := minio.New(endpoint, &minio.Options{
		Creds: credentials.NewStaticV4(
			getEnvOrDefault("TICKET_S3_ACCESS_KEY", "minioadmin"),
			getEnvOrDefault("TICKET_S3_SECRET_KEY", "minioadmin"), ""),
		Secure: os.Getenv("TICKET_S3_USE_SSL") == "true",
		Region: region,
	})
	if err != nil {
		l.Errorf("failed to create S3 client: %v", err)
		return nil, func() {}, err
	}

	bg := context.Background()
	if exists, err := client.BucketExists(bg, bucket); err != nil {
		l.Warnf("bucket existence check failed: %v", err)
	} else if !exists {
		if err := client.MakeBucket(bg, bucket, minio.MakeBucketOptions{Region: region}); err != nil {
			l.Warnf("failed to create bucket %s: %v", bucket, err)
		} else {
			l.Infof("created bucket %s", bucket)
		}
	}

	return &StorageClient{client: client, bucket: bucket, log: l}, func() {}, nil
}

// Upload stores content at key and returns the key.
func (s *StorageClient) Upload(ctx context.Context, key, contentType string, content []byte) error {
	_, err := s.client.PutObject(ctx, s.bucket, key, bytes.NewReader(content), int64(len(content)),
		minio.PutObjectOptions{ContentType: contentType})
	return err
}

// Open returns a reader for the object at key (caller closes it).
func (s *StorageClient) Open(ctx context.Context, key string) (io.ReadCloser, error) {
	obj, err := s.client.GetObject(ctx, s.bucket, key, minio.GetObjectOptions{})
	if err != nil {
		return nil, err
	}
	return obj, nil
}

// Delete removes the object at key.
func (s *StorageClient) Delete(ctx context.Context, key string) error {
	return s.client.RemoveObject(ctx, s.bucket, key, minio.RemoveObjectOptions{})
}
