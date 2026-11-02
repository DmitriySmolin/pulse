package checker

import (
	"bytes"
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	"pulse/internal/message"
)

// S3Config is read from S3_* variables. Empty Endpoint disables snapshots.
type S3Config struct {
	Endpoint        string
	Region          string
	Bucket          string
	AccessKeyID     string
	SecretAccessKey string
	ForcePathStyle  bool
}

// Snapshots uploads failed-check snapshots to S3-compatible storage (SeaweedFS).
type Snapshots struct {
	client *s3.Client
	bucket string
}

// NewSnapshots builds the client without reading ~/.aws or EC2 metadata:
// everything comes from S3Config.
func NewSnapshots(cfg S3Config) *Snapshots {
	var creds aws.CredentialsProvider = aws.AnonymousCredentials{}
	if cfg.AccessKeyID != "" {
		creds = credentials.NewStaticCredentialsProvider(cfg.AccessKeyID, cfg.SecretAccessKey, "")
	}
	client := s3.New(s3.Options{
		Region:       cfg.Region,
		BaseEndpoint: aws.String(cfg.Endpoint),
		Credentials:  creds,
		// SeaweedFS, Ceph and other S3 clones usually need path-style URLs
		// (http://s3:8333/bucket/key instead of http://bucket.s3:8333/key).
		UsePathStyle: cfg.ForcePathStyle,
		// Since 2025 the SDK adds CRC32 checksums to every request by default;
		// many S3-compatible servers reject that. Only send them when required.
		RequestChecksumCalculation: aws.RequestChecksumCalculationWhenRequired,
		ResponseChecksumValidation: aws.ResponseChecksumValidationWhenRequired,
	})
	return &Snapshots{client: client, bucket: cfg.Bucket}
}

// CheckBucket is called on start to warn early about a missing bucket.
func (s *Snapshots) CheckBucket(ctx context.Context) error {
	_, err := s.client.HeadBucket(ctx, &s3.HeadBucketInput{Bucket: aws.String(s.bucket)})
	return err
}

// Put uploads one snapshot.
func (s *Snapshots) Put(ctx context.Context, key string, body []byte) error {
	_, err := s.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket:      aws.String(s.bucket),
		Key:         aws.String(key),
		Body:        bytes.NewReader(body),
		ContentType: aws.String("text/plain; charset=utf-8"),
	})
	return err
}

// SnapshotKey is {monitor_id}/{check_id}.txt.
func SnapshotKey(monitorID, checkID int64) string {
	return fmt.Sprintf("%d/%d.txt", monitorID, checkID)
}

// FormatSnapshot renders the response as text: check metadata, status line,
// headers (sorted), an empty line and the first 64 KiB of the body.
func FormatSnapshot(req message.CheckRequested, checkID int64, res Result) []byte {
	var b strings.Builder
	fmt.Fprintf(&b, "monitor_id: %d\ncheck_id: %d\nrequest: %s %s\nchecked_at: %s\nlatency_ms: %d\nerror: %s\n\n",
		req.MonitorID, checkID, req.Method, req.URL, res.CheckedAt.Format(time.RFC3339Nano),
		res.Latency.Milliseconds(), res.Err)
	if res.StatusCode == 0 {
		b.WriteString("(no response)\n")
		return []byte(b.String())
	}
	fmt.Fprintf(&b, "%s %s\n", res.Proto, res.Status)
	for _, name := range slices.Sorted(maps.Keys(res.Header)) {
		for _, v := range res.Header[name] {
			fmt.Fprintf(&b, "%s: %s\n", name, v)
		}
	}
	b.WriteString("\n")
	b.Write(res.Body)
	return []byte(b.String())
}
