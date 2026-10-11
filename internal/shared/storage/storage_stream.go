package storage

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/aliyun/aliyun-oss-go-sdk/oss"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/minio/minio-go/v7"
	"github.com/tencentyun/cos-go-sdk-v5"
)

// IStreamingStorage 提供有界内存的对象传输，键名相对于配置的 BasePath
// 上传不会关闭调用方的 reader，下载流必须由调用方关闭
type IStreamingStorage interface {
	UploadStream(ctx context.Context, objectName string, reader io.ReadSeeker, size int64, contentType string) error
	DownloadStream(ctx context.Context, objectName string) (io.ReadCloser, error)
}

var (
	_ IStreamingStorage = (*S3Storage)(nil)
	_ IStreamingStorage = (*MinioStorage)(nil)
	_ IStreamingStorage = (*OSSStorage)(nil)
	_ IStreamingStorage = (*GCSStorage)(nil)
	_ IStreamingStorage = (*COSStorage)(nil)
)

// 隐藏底层文件的 Close 方法，防止 SDK 关闭调用方仍需持有的文件
type nonClosingReadSeeker struct{ io.ReadSeeker }

func validateStreamKey(ctx context.Context, objectName string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if objectName == "" || strings.HasPrefix(objectName, "/") || strings.ContainsAny(objectName, "\\\x00\r\n") || strings.Contains(objectName, "://") {
		return fmt.Errorf("object name must be a relative storage key")
	}
	for _, part := range strings.Split(objectName, "/") {
		if part == "." || part == ".." {
			return fmt.Errorf("object name cannot traverse storage directories")
		}
	}
	return nil
}

func validateStreamUpload(ctx context.Context, objectName string, reader io.ReadSeeker, size int64) error {
	if err := validateStreamKey(ctx, objectName); err != nil {
		return err
	}
	if reader == nil || size < 0 {
		return fmt.Errorf("upload requires a reader and a non-negative size")
	}
	start, err := reader.Seek(0, io.SeekCurrent)
	if err != nil {
		return fmt.Errorf("inspect upload position: %w", err)
	}
	end, err := reader.Seek(0, io.SeekEnd)
	if err != nil {
		return fmt.Errorf("inspect upload size: %w", err)
	}
	if _, err := reader.Seek(start, io.SeekStart); err != nil {
		return fmt.Errorf("restore upload position: %w", err)
	}
	if end-start != size {
		return fmt.Errorf("upload size mismatch: reader has %d bytes, expected %d", end-start, size)
	}
	return nil
}

// UploadStream 将数据流上传到 S3，不生成公开链接
func (s *S3Storage) UploadStream(ctx context.Context, objectName string, reader io.ReadSeeker, size int64, contentType string) error {
	if err := validateStreamUpload(ctx, objectName, reader, size); err != nil {
		return err
	}
	reader = nonClosingReadSeeker{reader}
	if s == nil || s.Client == nil || s.s == nil {
		return fmt.Errorf("S3 storage is not configured")
	}
	_, err := s.Client.PutObject(ctx, &s3.PutObjectInput{Bucket: aws.String(s.s.Bucket), Key: aws.String(getFullPath(s.s.BasePath, objectName)), Body: reader, ContentLength: aws.Int64(size), ContentType: aws.String(contentType)})
	return err
}

// DownloadStream 返回 S3 下载流并保留请求上下文
func (s *S3Storage) DownloadStream(ctx context.Context, objectName string) (io.ReadCloser, error) {
	if err := validateStreamKey(ctx, objectName); err != nil {
		return nil, err
	}
	if s == nil || s.Client == nil || s.s == nil {
		return nil, fmt.Errorf("S3 storage is not configured")
	}
	result, err := s.Client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(s.s.Bucket), Key: aws.String(getFullPath(s.s.BasePath, objectName))})
	if err != nil {
		return nil, err
	}
	return result.Body, nil
}

// UploadStream 将数据流上传到 MinIO，分片由 SDK 管理
func (m *MinioStorage) UploadStream(ctx context.Context, objectName string, reader io.ReadSeeker, size int64, contentType string) error {
	if err := validateStreamUpload(ctx, objectName, reader, size); err != nil {
		return err
	}
	reader = nonClosingReadSeeker{reader}
	if m == nil || m.Client == nil || m.s == nil {
		return fmt.Errorf("MinIO storage is not configured")
	}
	_, err := m.Client.PutObject(ctx, m.s.Bucket, getFullPath(m.s.BasePath, objectName), reader, size, minio.PutObjectOptions{ContentType: contentType, NumThreads: 1})
	return err
}

// DownloadStream 返回 MinIO 下载流，调用方负责读取错误和关闭流
func (m *MinioStorage) DownloadStream(ctx context.Context, objectName string) (io.ReadCloser, error) {
	if err := validateStreamKey(ctx, objectName); err != nil {
		return nil, err
	}
	if m == nil || m.Client == nil || m.s == nil {
		return nil, fmt.Errorf("MinIO storage is not configured")
	}
	return m.Client.GetObject(ctx, m.s.Bucket, getFullPath(m.s.BasePath, objectName), minio.GetObjectOptions{})
}

// UploadStream 使用可取消的上下文将数据上传到 OSS
func (o *OSSStorage) UploadStream(ctx context.Context, objectName string, reader io.ReadSeeker, size int64, contentType string) error {
	if err := validateStreamUpload(ctx, objectName, reader, size); err != nil {
		return err
	}
	reader = nonClosingReadSeeker{reader}
	if o == nil || o.Bucket == nil || o.s == nil {
		return fmt.Errorf("OSS storage is not configured")
	}
	return o.Bucket.PutObject(getFullPath(o.s.BasePath, objectName), reader, oss.WithContext(ctx), oss.ContentType(contentType), oss.ContentLength(size))
}

// DownloadStream 使用可取消的上下文返回 OSS 下载流
func (o *OSSStorage) DownloadStream(ctx context.Context, objectName string) (io.ReadCloser, error) {
	if err := validateStreamKey(ctx, objectName); err != nil {
		return nil, err
	}
	if o == nil || o.Bucket == nil || o.s == nil {
		return nil, fmt.Errorf("OSS storage is not configured")
	}
	return o.Bucket.GetObject(getFullPath(o.s.BasePath, objectName), oss.WithContext(ctx))
}

// UploadStream 将数据写入 GCS，在读取失败时中止对象写入
func (g *GCSStorage) UploadStream(ctx context.Context, objectName string, reader io.ReadSeeker, size int64, contentType string) error {
	if err := validateStreamUpload(ctx, objectName, reader, size); err != nil {
		return err
	}
	reader = nonClosingReadSeeker{reader}
	if g == nil || g.Bucket == nil || g.s == nil {
		return fmt.Errorf("GCS storage is not configured")
	}
	writer := g.Bucket.Object(getFullPath(g.s.BasePath, objectName)).NewWriter(ctx)
	writer.ContentType = contentType
	writer.ChunkSize = defaultPartSize
	if _, err := io.CopyN(writer, reader, size); err != nil {
		_ = writer.CloseWithError(err)
		return err
	}
	return writer.Close()
}

// DownloadStream 返回使用调用方上下文的 GCS 下载流
func (g *GCSStorage) DownloadStream(ctx context.Context, objectName string) (io.ReadCloser, error) {
	if err := validateStreamKey(ctx, objectName); err != nil {
		return nil, err
	}
	if g == nil || g.Bucket == nil || g.s == nil {
		return nil, fmt.Errorf("GCS storage is not configured")
	}
	return g.Bucket.Object(getFullPath(g.s.BasePath, objectName)).NewReader(ctx)
}

// UploadStream 使用可取消的上下文将数据上传到 COS
func (c *COSStorage) UploadStream(ctx context.Context, objectName string, reader io.ReadSeeker, size int64, contentType string) error {
	if err := validateStreamUpload(ctx, objectName, reader, size); err != nil {
		return err
	}
	reader = nonClosingReadSeeker{reader}
	if c == nil || c.Client == nil || c.s == nil {
		return fmt.Errorf("COS storage is not configured")
	}
	_, err := c.Client.Object.Put(ctx, getFullPath(c.s.BasePath, objectName), reader, &cos.ObjectPutOptions{ObjectPutHeaderOptions: &cos.ObjectPutHeaderOptions{ContentType: contentType, ContentLength: size}})
	return err
}

// DownloadStream 返回使用调用方上下文的 COS 下载流
func (c *COSStorage) DownloadStream(ctx context.Context, objectName string) (io.ReadCloser, error) {
	if err := validateStreamKey(ctx, objectName); err != nil {
		return nil, err
	}
	if c == nil || c.Client == nil || c.s == nil {
		return nil, fmt.Errorf("COS storage is not configured")
	}
	result, err := c.Client.Object.Get(ctx, getFullPath(c.s.BasePath, objectName), nil)
	if err != nil {
		return nil, err
	}
	return result.Body, nil
}
