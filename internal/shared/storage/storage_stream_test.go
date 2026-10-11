package storage

import (
	"context"
	"errors"
	"hash/crc64"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	gcs "cloud.google.com/go/storage"
	"github.com/aliyun/aliyun-oss-go-sdk/oss"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/minio/minio-go/v7"
	miniocredentials "github.com/minio/minio-go/v7/pkg/credentials"
	"github.com/tencentyun/cos-go-sdk-v5"
	"google.golang.org/api/option"
)

type storageContextKey struct{}
type storageRoundTripper func(*http.Request) (*http.Response, error)

func (f storageRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func TestProviderStreamingAdapters(t *testing.T) {
	for _, provider := range []string{"s3", "minio", "oss", "gcs", "cos"} {
		t.Run(provider, func(t *testing.T) {
			ctx := context.WithValue(context.Background(), storageContextKey{}, "preserved")
			var mu sync.Mutex
			var paths []string
			var uploads []string
			transport := storageRoundTripper(func(req *http.Request) (*http.Response, error) {
				if req.Context().Value(storageContextKey{}) != "preserved" {
					t.Error("request context was discarded")
				}
				mu.Lock()
				defer mu.Unlock()
				paths = append(paths, req.URL.Path)
				headers := make(http.Header)
				headers.Set("Content-Type", "application/octet-stream")
				headers.Set("x-cos-hash-crc64ecma", strconv.FormatUint(crc64.Checksum([]byte("payload"), crc64.MakeTable(crc64.ECMA)), 10))
				headers.Set("ETag", `"321c3cf486ed509164edec1e1981fec8"`)
				headers.Set("Last-Modified", time.Now().UTC().Format(http.TimeFormat))
				body := "payload"
				if req.Method == http.MethodPut || req.Method == http.MethodPost {
					if req.Body != nil {
						data, err := io.ReadAll(req.Body)
						_ = req.Body.Close()
						if err != nil {
							return nil, err
						}
						uploads = append(uploads, string(data))
					}
					body = ""
					if provider == "gcs" {
						body = `{"bucket":"bucket","name":"base/key","size":"7"}`
						headers.Set("Content-Type", "application/json")
					}
				}
				headers.Set("Content-Length", strconv.Itoa(len(body)))
				return &http.Response{StatusCode: 200, Header: headers, Body: io.NopCloser(strings.NewReader(body)), ContentLength: int64(len(body)), Request: req}, nil
			})
			httpClient := &http.Client{Transport: transport}
			cfg := &Storage{Bucket: "bucket", BasePath: "base"}
			var adapter IStreamingStorage
			switch provider {
			case "s3":
				client := s3.NewFromConfig(aws.Config{Region: "us-east-1", Credentials: credentials.NewStaticCredentialsProvider("key", "secret", ""), HTTPClient: httpClient, RequestChecksumCalculation: aws.RequestChecksumCalculationWhenRequired}, func(o *s3.Options) {
					o.BaseEndpoint = aws.String("https://storage.example.invalid")
					o.UsePathStyle = true
				})
				adapter = &S3Storage{Client: client, s: cfg}
			case "minio":
				client, err := minio.New("storage.example.invalid", &minio.Options{Creds: miniocredentials.NewStaticV4("key", "secret", ""), Secure: true, Region: "us-east-1", Transport: transport})
				if err != nil {
					t.Fatal(err)
				}
				adapter = &MinioStorage{Client: client, s: cfg}
			case "oss":
				client, err := oss.New("https://storage.example.invalid", "key", "secret", oss.HTTPClient(httpClient), oss.ForcePathStyle(true), oss.EnableCRC(false))
				if err != nil {
					t.Fatal(err)
				}
				bucket, err := client.Bucket("bucket")
				if err != nil {
					t.Fatal(err)
				}
				adapter = &OSSStorage{Client: client, Bucket: bucket, s: cfg}
			case "gcs":
				client, err := gcs.NewClient(ctx, option.WithHTTPClient(httpClient), option.WithEndpoint("https://storage.example.invalid"), option.WithoutAuthentication())
				if err != nil {
					t.Fatal(err)
				}
				defer client.Close()
				adapter = &GCSStorage{Client: client, Bucket: client.Bucket("bucket"), s: cfg}
			case "cos":
				endpoint, _ := url.Parse("https://storage.example.invalid")
				adapter = &COSStorage{Client: cos.NewClient(&cos.BaseURL{BucketURL: endpoint}, httpClient), s: cfg}
			}
			uploadReader := &closeTrackingReader{Reader: strings.NewReader("payload")}
			if err := adapter.UploadStream(ctx, "key", uploadReader, 7, "application/octet-stream"); err != nil {
				t.Fatal(err)
			}
			if uploadReader.closed {
				t.Fatal("provider closed the caller's reader")
			}
			stream, err := adapter.DownloadStream(ctx, "key")
			if err != nil {
				t.Fatal(err)
			}
			data, err := io.ReadAll(stream)
			if err != nil {
				t.Fatal(err)
			}
			if err := stream.Close(); err != nil {
				t.Fatal(err)
			}
			if string(data) != "payload" {
				t.Fatalf("wrong download: %q", data)
			}
			mu.Lock()
			defer mu.Unlock()
			if len(uploads) == 0 || !strings.Contains(uploads[0], "payload") {
				t.Fatalf("upload body lost: %q", uploads)
			}
			found := false
			for _, path := range paths {
				if strings.Contains(path, "base/base") {
					t.Fatalf("BasePath applied twice: %s", path)
				}
				if strings.Contains(path, "base/key") || strings.Contains(path, "base%2Fkey") {
					found = true
				}
			}
			if !found {
				t.Fatalf("BasePath missing in requests: %v", paths)
			}
		})
	}
}

func TestStreamingValidationBeforeNetwork(t *testing.T) {
	adapters := []IStreamingStorage{&S3Storage{}, &MinioStorage{}, &OSSStorage{}, &GCSStorage{}, &COSStorage{}}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	for _, adapter := range adapters {
		if err := adapter.UploadStream(cancelled, "key", strings.NewReader("x"), 1, ""); !errors.Is(err, context.Canceled) {
			t.Errorf("%T upload lost cancellation: %v", adapter, err)
		}
		if _, err := adapter.DownloadStream(cancelled, "key"); !errors.Is(err, context.Canceled) {
			t.Errorf("%T download lost cancellation: %v", adapter, err)
		}
		if err := adapter.UploadStream(context.Background(), "key", strings.NewReader("x"), 2, ""); err == nil {
			t.Errorf("%T accepted incorrect length", adapter)
		}
		for _, key := range []string{"", "../escape", "base/../../escape", "/absolute", "https://example.invalid/key", "base\\escape"} {
			if _, err := adapter.DownloadStream(context.Background(), key); err == nil {
				t.Errorf("%T accepted unsafe key %q", adapter, key)
			}
		}
	}
}

type closeTrackingReader struct {
	*strings.Reader
	closed bool
}

func (r *closeTrackingReader) Close() error { r.closed = true; return nil }
