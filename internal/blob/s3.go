package blob

import (
	"context"
	"fmt"
	"io"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

type S3Config struct {
	Endpoint  string
	Bucket    string
	AccessKey string
	SecretKey string
	Region    string
	UseSSL    bool
}

type S3Store struct {
	client *minio.Client
	bucket string
}

func NewS3Store(cfg S3Config) (*S3Store, error) {
	if cfg.Endpoint == "" || cfg.Bucket == "" || cfg.AccessKey == "" || cfg.SecretKey == "" {
		return nil, fmt.Errorf("blob s3 endpoint, bucket, access key, and secret key are required")
	}
	client, err := minio.New(cfg.Endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(cfg.AccessKey, cfg.SecretKey, ""),
		Secure: cfg.UseSSL,
		Region: cfg.Region,
	})
	if err != nil {
		return nil, err
	}
	return &S3Store{client: client, bucket: cfg.Bucket}, nil
}

func (s *S3Store) Put(ctx context.Context, key string, r io.Reader) error {
	clean, err := ValidateKey(key)
	if err != nil {
		return err
	}
	_, err = s.client.PutObject(ctx, s.bucket, clean, r, -1, minio.PutObjectOptions{})
	return err
}

func (s *S3Store) Open(ctx context.Context, key string) (io.ReadCloser, error) {
	clean, err := ValidateKey(key)
	if err != nil {
		return nil, err
	}
	return s.client.GetObject(ctx, s.bucket, clean, minio.GetObjectOptions{})
}

func (s *S3Store) Delete(ctx context.Context, key string) error {
	clean, err := ValidateKey(key)
	if err != nil {
		return err
	}
	return s.client.RemoveObject(ctx, s.bucket, clean, minio.RemoveObjectOptions{})
}
