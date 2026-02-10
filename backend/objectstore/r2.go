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

// R2Client is a Cloudflare R2 (S3-compatible) storage client.
type R2Client struct {
	client    *s3.Client
	bucket    string
	publicURL string
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

// NewR2Client creates an R2 storage client pointed at the given Cloudflare account.
func NewR2Client(accountID, accessKeyID, accessKeySecret, bucketName, publicURL string) (*R2Client, error) {
	endpoint := fmt.Sprintf("https://%s.r2.cloudflarestorage.com", accountID)

	client := s3.New(s3.Options{
		BaseEndpoint: aws.String(endpoint),
		Region:       "auto",
		Credentials:  aws.NewCredentialsCache(credentials.NewStaticCredentialsProvider(accessKeyID, accessKeySecret, "")),
		UsePathStyle: true,
	})

	log.Debug().
		Str("endpoint", endpoint).
		Str("bucket", bucketName).
		Msg("R2 client created")

	return &R2Client{
		client:    client,
		bucket:    bucketName,
		publicURL: publicURL,
	}, nil
}

// NewR2ClientFromEnv creates an R2 client from environment variables:
// R2_ACCOUNT_ID, R2_ACCESS_KEY_ID, R2_ACCESS_KEY_SECRET, R2_BUCKET_NAME, R2_PUBLIC_URL.
func NewR2ClientFromEnv() (*R2Client, error) {
	vars := map[string]string{
		"R2_ACCOUNT_ID":        os.Getenv("R2_ACCOUNT_ID"),
		"R2_ACCESS_KEY_ID":     os.Getenv("R2_ACCESS_KEY_ID"),
		"R2_ACCESS_KEY_SECRET": os.Getenv("R2_ACCESS_KEY_SECRET"),
		"R2_BUCKET_NAME":       os.Getenv("R2_BUCKET_NAME"),
		"R2_PUBLIC_URL":        os.Getenv("R2_PUBLIC_URL"),
	}

	for name, val := range vars {
		if val == "" {
			return nil, fmt.Errorf("required environment variable %s is not set", name)
		}
	}

	return NewR2Client(
		vars["R2_ACCOUNT_ID"],
		vars["R2_ACCESS_KEY_ID"],
		vars["R2_ACCESS_KEY_SECRET"],
		vars["R2_BUCKET_NAME"],
		vars["R2_PUBLIC_URL"],
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
