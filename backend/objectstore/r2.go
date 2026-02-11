package objectstore

import (
	"bytes"
	"context"
	"fmt"
	"os"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/rs/zerolog/log"
)

// ObjectStore defines the interface for object storage operations.
type ObjectStore interface {
	Upload(ctx context.Context, key string, data []byte, contentType string) (string, error)
	Delete(ctx context.Context, key string) error
}

// R2Client is an S3-compatible storage client (works with Cloudflare R2, MinIO, etc).
type R2Client struct {
	client    *s3.Client
	bucket    string
	publicURL string // base URL for public access (e.g. https://pub-xxx.r2.dev)
}

// NewR2ClientWithS3 creates an R2Client wrapping an existing S3 client.
// Useful for testing with a fake S3 backend.
func NewR2ClientWithS3(client *s3.Client, bucketName, publicURL string) *R2Client {
	return &R2Client{
		client:    client,
		bucket:    bucketName,
		publicURL: publicURL,
	}
}

// NewR2Client creates an S3-compatible storage client.
// endpoint is the S3 API URL; publicURL is the base URL for serving files publicly.
func NewR2Client(endpoint, accessKey, secretKey, bucketName, publicURL string) (*R2Client, error) {
	client := s3.New(s3.Options{
		BaseEndpoint: aws.String(endpoint),
		Region:       "auto",
		Credentials:  aws.NewCredentialsCache(credentials.NewStaticCredentialsProvider(accessKey, secretKey, "")),
		UsePathStyle: true,
	})

	log.Debug().
		Str("endpoint", endpoint).
		Str("public_url", publicURL).
		Str("bucket", bucketName).
		Msg("storage client created")

	return &R2Client{
		client:    client,
		bucket:    bucketName,
		publicURL: publicURL,
	}, nil
}

// NewR2ClientFromEnv creates a storage client from environment variables:
// STORAGE_ENDPOINT, STORAGE_ACCESS_KEY, STORAGE_SECRET_KEY, STORAGE_BUCKET, STORAGE_PUBLIC_URL.
func NewR2ClientFromEnv() (*R2Client, error) {
	vars := map[string]string{
		"STORAGE_ENDPOINT":   os.Getenv("STORAGE_ENDPOINT"),
		"STORAGE_ACCESS_KEY": os.Getenv("STORAGE_ACCESS_KEY"),
		"STORAGE_SECRET_KEY": os.Getenv("STORAGE_SECRET_KEY"),
		"STORAGE_BUCKET":     os.Getenv("STORAGE_BUCKET"),
		"STORAGE_PUBLIC_URL": os.Getenv("STORAGE_PUBLIC_URL"),
	}

	for name, val := range vars {
		if val == "" {
			return nil, fmt.Errorf("required environment variable %s is not set", name)
		}
	}

	return NewR2Client(
		vars["STORAGE_ENDPOINT"],
		vars["STORAGE_ACCESS_KEY"],
		vars["STORAGE_SECRET_KEY"],
		vars["STORAGE_BUCKET"],
		vars["STORAGE_PUBLIC_URL"],
	)
}

// Upload puts an object into the R2 bucket and returns its public URL.
func (r *R2Client) Upload(ctx context.Context, key string, data []byte, contentType string) (string, error) {
	log.Debug().
		Str("bucket", r.bucket).
		Str("key", key).
		Int("size", len(data)).
		Str("content_type", contentType).
		Msg("uploading object to R2")

	_, err := r.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket:      aws.String(r.bucket),
		Key:         aws.String(key),
		Body:        bytes.NewReader(data),
		ContentType: aws.String(contentType),
	})
	if err != nil {
		return "", fmt.Errorf("r2 upload %q: %w", key, err)
	}

	url := r.publicURL + "/" + key
	log.Debug().
		Str("url", url).
		Msg("object uploaded to R2")

	return url, nil
}

// Delete removes an object from the R2 bucket.
func (r *R2Client) Delete(ctx context.Context, key string) error {
	log.Debug().
		Str("bucket", r.bucket).
		Str("key", key).
		Msg("deleting object from R2")

	_, err := r.client.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: aws.String(r.bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		return fmt.Errorf("r2 delete %q: %w", key, err)
	}

	return nil
}
