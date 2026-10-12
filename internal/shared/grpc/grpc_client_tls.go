package grpc

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"io"
	"net"
	"os"
	"strconv"
	"strings"

	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
)

// ClientTLSConfig 配置严格服务器身份验证，不允许跳过证书或主机名校验
type ClientTLSConfig struct {
	Enabled    bool   `mapstructure:"enabled" json:"enabled"`
	CAFile     string `mapstructure:"caFile" json:"caFile"`
	ServerName string `mapstructure:"serverName" json:"serverName"`
}

func clientTransportCredentials(ctx context.Context, cfg ClientTLSConfig) (credentials.TransportCredentials, string, error) {
	if err := ctx.Err(); err != nil {
		return nil, "", err
	}
	if !cfg.Enabled {
		if cfg.CAFile != "" || cfg.ServerName != "" {
			return nil, "", errors.New("gRPC TLS options require TLS enabled")
		}
		return insecure.NewCredentials(), "", nil
	}
	if cfg.ServerName != strings.TrimSpace(cfg.ServerName) || strings.ContainsAny(cfg.ServerName, "/\\\x00\r\n\t ") {
		return nil, "", errors.New("invalid gRPC TLS server name")
	}
	trustDigest := ""
	var roots *x509.CertPool
	if cfg.CAFile != "" {
		file, err := os.Open(cfg.CAFile)
		if err != nil {
			return nil, "", errors.New("cannot open gRPC TLS CA file")
		}
		defer file.Close()
		info, err := file.Stat()
		if err != nil || !info.Mode().IsRegular() || info.Size() > 1<<20 {
			return nil, "", errors.New("invalid gRPC TLS CA file")
		}
		data, err := io.ReadAll(io.LimitReader(file, (1<<20)+1))
		if err != nil || len(data) > 1<<20 {
			return nil, "", errors.New("cannot read gRPC TLS CA file")
		}
		if err := ctx.Err(); err != nil {
			return nil, "", err
		}
		sum := sha256.Sum256(data)
		trustDigest = hex.EncodeToString(sum[:])
		roots = x509.NewCertPool()
		if !roots.AppendCertsFromPEM(data) {
			return nil, "", errors.New("gRPC TLS CA file contains no valid certificates")
		}
	}
	return credentials.NewTLS(&tls.Config{MinVersion: tls.VersionTLS12, RootCAs: roots, ServerName: cfg.ServerName}), trustDigest, nil
}

// ControllerAuthenticated 表示该客户端强制认证服务器证书，收到的 RPC 响应不可通过明文降级
func (c *ClientWrapper) ControllerAuthenticated() bool {
	if c == nil {
		return false
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.controllerAuthenticated && c.conn != nil
}

func normalizeClientAddress(value string) string {
	if strings.HasPrefix(value, ":") {
		return "localhost" + value
	}
	return value
}

// ControllerIdentityBinding 绑定客户端实际加载的认证端点和显式 CA 内容，不读取可在运行中替换的配置文件
// 持久身份恢复必须使用显式 CA；系统根仍可用于不保存身份的普通 TLS 客户端
func (c *ClientWrapper) ControllerIdentityBinding() (string, error) {
	if c == nil {
		return "", errors.New("authenticated controller identity unavailable")
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	if !c.controllerAuthenticated || c.conn == nil || c.controllerTrustDigest == "" {
		return "", errors.New("identity persistence requires verified TLS with an explicit CA file")
	}
	host, port, err := net.SplitHostPort(normalizeClientAddress(c.config.ServerAddr))
	number, portErr := strconv.Atoi(port)
	if err != nil || portErr != nil || number < 1 || number > 65535 || host == "" || strings.ContainsAny(host, "/\\ @\x00\r\n") {
		return "", errors.New("identity persistence requires a canonical controller host and port")
	}
	host = strings.ToLower(host)
	if ip := net.ParseIP(host); ip != nil {
		host = ip.String()
	}
	name := c.config.TLS.ServerName
	if name == "" {
		name = host
	}
	name = strings.ToLower(name)
	digest := sha256.New()
	for _, value := range []string{"arcentra-agent-identity-controller-v1", net.JoinHostPort(host, strconv.Itoa(number)), name, c.controllerTrustDigest} {
		_, _ = digest.Write(binary.BigEndian.AppendUint64(nil, uint64(len(value))))
		_, _ = digest.Write([]byte(value))
	}
	return hex.EncodeToString(digest.Sum(nil)), nil
}
