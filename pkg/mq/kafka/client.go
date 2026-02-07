package kafka

import (
	"github.com/arcentrix/arcentra/pkg/mq"
	"github.com/confluentinc/confluent-kafka-go/v2/kafka"
)

// ClientConfig represents Kafka shared client configuration.
type ClientConfig struct {
	BootstrapServers string `json:"bootstrapServers" mapstructure:"bootstrapServers"`
	ClientId         string `json:"clientId" mapstructure:"clientId"`

	SecurityProtocol string `json:"securityProtocol" mapstructure:"securityProtocol"`
	SaslMechanism    string `json:"saslMechanism" mapstructure:"saslMechanism"`
	SaslUsername     string `json:"saslUsername" mapstructure:"saslUsername"`
	SaslPassword     string `json:"saslPassword" mapstructure:"saslPassword"`
	SslCaFile        string `json:"sslCaFile" mapstructure:"sslCaFile"`
	SslCertFile      string `json:"sslCertFile" mapstructure:"sslCertFile"`
	SslKeyFile       string `json:"sslKeyFile" mapstructure:"sslKeyFile"`
	SslPassword      string `json:"sslPassword" mapstructure:"sslPassword"`
}

// ClientOption defines optional configuration for ClientConfig.
type ClientOption interface {
	apply(*ClientConfig)
}

type clientOptionFunc func(*ClientConfig)

func (fn clientOptionFunc) apply(cfg *ClientConfig) {
	fn(cfg)
}

func WithClientId(clientId string) ClientOption {
	return clientOptionFunc(func(cfg *ClientConfig) {
		cfg.ClientId = clientId
	})
}

func WithSecurityProtocol(securityProtocol string) ClientOption {
	return clientOptionFunc(func(cfg *ClientConfig) {
		cfg.SecurityProtocol = securityProtocol
	})
}

func WithSaslMechanism(mechanism string) ClientOption {
	return clientOptionFunc(func(cfg *ClientConfig) {
		cfg.SaslMechanism = mechanism
	})
}

func WithSaslUsername(username string) ClientOption {
	return clientOptionFunc(func(cfg *ClientConfig) {
		cfg.SaslUsername = username
	})
}

func WithSaslPassword(password string) ClientOption {
	return clientOptionFunc(func(cfg *ClientConfig) {
		cfg.SaslPassword = password
	})
}

func WithSslCaFile(path string) ClientOption {
	return clientOptionFunc(func(cfg *ClientConfig) {
		cfg.SslCaFile = path
	})
}

func WithSslCertFile(path string) ClientOption {
	return clientOptionFunc(func(cfg *ClientConfig) {
		cfg.SslCertFile = path
	})
}

func WithSslKeyFile(path string) ClientOption {
	return clientOptionFunc(func(cfg *ClientConfig) {
		cfg.SslKeyFile = path
	})
}

func WithSslPassword(password string) ClientOption {
	return clientOptionFunc(func(cfg *ClientConfig) {
		cfg.SslPassword = password
	})
}

// KafkaClient holds a base client configuration.
type KafkaClient struct {
	Config ClientConfig
}

// NewKafkaClient creates a new KafkaClient using options.
func NewKafkaClient(bootstrapServers string, opts ...ClientOption) (*KafkaClient, error) {
	cfg := ClientConfig{
		BootstrapServers: bootstrapServers,
	}
	for _, opt := range opts {
		opt.apply(&cfg)
	}
	if err := mq.RequireNonEmpty("bootstrapServers", cfg.BootstrapServers); err != nil {
		return nil, err
	}
	return &KafkaClient{Config: cfg}, nil
}

func buildBaseConfig(cfg ClientConfig) (*kafka.ConfigMap, error) {
	if err := mq.RequireNonEmpty("bootstrapServers", cfg.BootstrapServers); err != nil {
		return nil, err
	}

	config := &kafka.ConfigMap{
		"bootstrap.servers": cfg.BootstrapServers,
	}

	if cfg.ClientId != "" {
		_ = config.SetKey("client.id", cfg.ClientId)
	}

	applyAuthConfig(config, cfg)

	return config, nil
}

func applyAuthConfig(config *kafka.ConfigMap, cfg ClientConfig) {
	if cfg.SecurityProtocol != "" {
		_ = config.SetKey("security.protocol", cfg.SecurityProtocol)
	}
	if cfg.SaslMechanism != "" {
		_ = config.SetKey("sasl.mechanism", cfg.SaslMechanism)
	}
	if cfg.SaslUsername != "" {
		_ = config.SetKey("sasl.username", cfg.SaslUsername)
	}
	if cfg.SaslPassword != "" {
		_ = config.SetKey("sasl.password", cfg.SaslPassword)
	}
	if cfg.SslCaFile != "" {
		_ = config.SetKey("ssl.ca.location", cfg.SslCaFile)
	}
	if cfg.SslCertFile != "" {
		_ = config.SetKey("ssl.certificate.location", cfg.SslCertFile)
	}
	if cfg.SslKeyFile != "" {
		_ = config.SetKey("ssl.key.location", cfg.SslKeyFile)
	}
	if cfg.SslPassword != "" {
		_ = config.SetKey("ssl.key.password", cfg.SslPassword)
	}
}
