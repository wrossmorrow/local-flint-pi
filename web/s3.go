package main

import (
	"bytes"
	"context"
	"fmt"
	"io"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// sessionStore is the object-storage sink for uploaded session parts. The
// production implementation pushes gzipped JSONL parts to S3; tests substitute
// an in-memory fake.
type sessionStore interface {
	// Put writes data to key with the given content type.
	Put(ctx context.Context, key string, data []byte, contentType string) error
	// Get reads the object at key, if present.
	Get(ctx context.Context, key string) ([]byte, error)
}

// staticCredentials is a CredentialsProvider that always returns the given
// static AWS credentials, bypassing the shared credential chain.
type staticCredentials struct {
	accessKey  string
	secretKey  string
	sessionKey string
}

func (c staticCredentials) Retrieve(_ context.Context) (aws.Credentials, error) {
	return aws.Credentials{
		AccessKeyID:     c.accessKey,
		SecretAccessKey: c.secretKey,
		SessionToken:    c.sessionKey,
	}, nil
}

// s3Store uploads session parts to an S3 bucket via the AWS SDK. A single
// PutObject is enough because parts are kept at or under the configured size
// limit, so no S3 multipart upload is used.
type s3Store struct {
	bucket string
	client *s3.Client
}

// newS3Store returns an S3 store for bucket using the given static AWS
// credentials and region.
func newS3Store(ctx context.Context, bucket, accessKey, secretKey, region string) (*s3Store, error) {
	client := s3.NewFromConfig(aws.Config{
		Region:      region,
		Credentials: staticCredentials{accessKey: accessKey, secretKey: secretKey, sessionKey: ""},
	}, func(o *s3.Options) {
		o.UsePathStyle = true
	})
	return &s3Store{bucket: bucket, client: client}, nil
}

// Put uploads data to the store's bucket at key.
func (s *s3Store) Put(ctx context.Context, key string, data []byte, contentType string) error {
	_, err := s.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket:      aws.String(s.bucket),
		Key:         aws.String(key),
		Body:        bytes.NewReader(data),
		ContentType: aws.String(contentType),
	})
	if err != nil {
		return fmt.Errorf("s3 put %s: %w", key, err)
	}
	return nil
}

func (s *s3Store) Get(ctx context.Context, key string) ([]byte, error) {
	out, err := s.client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		return nil, fmt.Errorf("s3 get %s: %w", key, err)
	}
	defer func() { _ = out.Body.Close() }()
	return io.ReadAll(out.Body)
}
