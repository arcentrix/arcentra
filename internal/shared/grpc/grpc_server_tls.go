package grpc

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"os"
	"time"

	"google.golang.org/grpc/credentials"
)

// ServerTLSConfig 配置运维提供的服务器证书和私钥，使用 TLS 1.2 及以上
// 禁用 TLS 时保留仅适用于受信网络的旧版明文模式
type ServerTLSConfig struct {
	Enabled  bool   `mapstructure:"enabled"`
	CertFile string `mapstructure:"certFile" json:"-"`
	KeyFile  string `mapstructure:"keyFile" json:"-"`
}

func serverTransportCredentials(ctx context.Context, cfg ServerTLSConfig) (credentials.TransportCredentials, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !cfg.Enabled {
		if cfg.CertFile != "" || cfg.KeyFile != "" {
			return nil, errors.New("gRPC server certificate options require TLS enabled")
		}
		return nil, nil
	}
	if cfg.CertFile == "" || cfg.KeyFile == "" {
		return nil, errors.New("gRPC server TLS requires both certFile and keyFile")
	}
	certPEM, err := readServerTLSFile(ctx, cfg.CertFile)
	if err != nil {
		return nil, errors.New("cannot read gRPC server TLS certificate")
	}
	keyPEM, err := readServerTLSFile(ctx, cfg.KeyFile)
	if err != nil {
		return nil, errors.New("cannot read gRPC server TLS private key")
	}
	defer clear(keyPEM)
	pair, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return nil, errors.New("invalid gRPC server TLS certificate/key pair")
	}
	now := time.Now()
	for _, der := range pair.Certificate {
		cert, parseErr := x509.ParseCertificate(der)
		if parseErr != nil || now.Before(cert.NotBefore) || !now.Before(cert.NotAfter) {
			return nil, errors.New("gRPC server TLS certificate is invalid or outside its validity period")
		}
	}
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		return nil, errors.New("invalid gRPC server TLS leaf certificate")
	}
	if len(leaf.ExtKeyUsage) > 0 {
		serverUsage := false
		for _, usage := range leaf.ExtKeyUsage {
			serverUsage = serverUsage || usage == x509.ExtKeyUsageAny || usage == x509.ExtKeyUsageServerAuth
		}
		if !serverUsage {
			return nil, errors.New("gRPC server TLS certificate does not allow server authentication")
		}
	}
	return credentials.NewTLS(&tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{pair}}), nil
}

func readServerTLSFile(ctx context.Context, path string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	const maxBytes = 1 << 20
	// 先拒绝特殊文件，避免读取命名管道或设备时无限等待
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() > maxBytes {
		return nil, errors.New("invalid TLS file")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	info, err = file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > maxBytes {
		return nil, errors.New("invalid TLS file")
	}
	data, err := io.ReadAll(io.LimitReader(file, maxBytes+1))
	if err != nil || len(data) > maxBytes {
		return nil, errors.New("cannot read TLS file")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return data, nil
}
