module github.com/arcentrix/arcentra

go 1.25.6

require (
	github.com/aws/aws-sdk-go-v2 v1.39.3
	github.com/aws/aws-sdk-go-v2/config v1.31.14
	github.com/aws/aws-sdk-go-v2/credentials v1.18.18
	github.com/aws/aws-sdk-go-v2/service/s3 v1.88.6
	github.com/davecgh/go-spew v1.1.2-0.20180830191138-d8f796af33cc // indirect
	github.com/expr-lang/expr v1.17.7
	github.com/fsnotify/fsnotify v1.9.0
	github.com/go-resty/resty/v2 v2.16.5
	github.com/gofiber/fiber/v2 v2.52.11
	github.com/gofiber/websocket/v2 v2.2.1
	github.com/golang-jwt/jwt/v5 v5.3.0
	github.com/google/uuid v1.6.0
	github.com/google/wire v0.7.0
	github.com/grpc-ecosystem/go-grpc-middleware v1.4.0
	github.com/minio/minio-go/v7 v7.0.95
	github.com/oklog/ulid/v2 v2.1.1
	github.com/prometheus/client_golang v1.23.2
	github.com/redis/go-redis/v9 v9.14.1
	github.com/robfig/cron v1.2.0
	github.com/spf13/cobra v1.10.1
	github.com/spf13/viper v1.21.0
	github.com/stretchr/testify v1.11.1
	github.com/teris-io/shortid v0.0.0-20220617161101-71ec9f2aa569
	github.com/timandy/routine v1.1.6
	go.opentelemetry.io/otel v1.39.0
	go.opentelemetry.io/otel/trace v1.39.0
	go.uber.org/zap v1.27.1
	golang.org/x/crypto v0.47.0
	golang.org/x/net v0.49.0 // indirect
	golang.org/x/oauth2 v0.32.0
	google.golang.org/api v0.157.0
	google.golang.org/grpc v1.77.0
	gopkg.in/natefinch/lumberjack.v2 v2.2.1
	gorm.io/driver/mysql v1.6.0
	gorm.io/gorm v1.31.0
	sigs.k8s.io/yaml v1.6.0
)

require (
	github.com/VictoriaMetrics/fastcache v1.13.2
	github.com/apache/rocketmq-client-go/v2 v2.1.2
	github.com/confluentinc/confluent-kafka-go/v2 v2.3.0
	github.com/containerd/containerd v1.7.29
	github.com/gofiber/contrib/fiberi18n/v2 v2.0.6
	github.com/hashicorp/go-metrics v0.5.4
	github.com/opencontainers/runtime-spec v1.1.0
	go.opentelemetry.io/otel/exporters/otlp/otlptrace v1.39.0
	go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc v1.39.0
	go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp v1.39.0
	go.opentelemetry.io/otel/sdk v1.39.0
	gorm.io/plugin/dbresolver v1.6.2
)

require (
	cloud.google.com/go v0.112.0 // indirect
	cloud.google.com/go/compute/metadata v0.9.0 // indirect
	cloud.google.com/go/iam v1.1.5 // indirect
	cyphar.com/go-pathrs v0.2.1 // indirect
	github.com/AdaLogics/go-fuzz-headers v0.0.0-20230811130428-ced1acdcaa24 // indirect
	github.com/AdamKorcz/go-118-fuzz-build v0.0.0-20230306123547-8075edf89bb0 // indirect
	github.com/Azure/go-ntlmssp v0.0.0-20221128193559-754e69321358 // indirect
	github.com/Microsoft/go-winio v0.6.2 // indirect
	github.com/Microsoft/hcsshim v0.11.7 // indirect
	github.com/andybalholm/brotli v1.2.0 // indirect
	github.com/aws/aws-sdk-go-v2/aws/protocol/eventstream v1.7.2 // indirect
	github.com/aws/aws-sdk-go-v2/feature/ec2/imds v1.18.10 // indirect
	github.com/aws/aws-sdk-go-v2/internal/configsources v1.4.10 // indirect
	github.com/aws/aws-sdk-go-v2/internal/endpoints/v2 v2.7.10 // indirect
	github.com/aws/aws-sdk-go-v2/internal/ini v1.8.4 // indirect
	github.com/aws/aws-sdk-go-v2/internal/v4a v1.4.10 // indirect
	github.com/aws/aws-sdk-go-v2/service/internal/accept-encoding v1.13.2 // indirect
	github.com/aws/aws-sdk-go-v2/service/internal/checksum v1.9.1 // indirect
	github.com/aws/aws-sdk-go-v2/service/internal/presigned-url v1.13.10 // indirect
	github.com/aws/aws-sdk-go-v2/service/internal/s3shared v1.19.10 // indirect
	github.com/aws/aws-sdk-go-v2/service/sso v1.29.7 // indirect
	github.com/aws/aws-sdk-go-v2/service/ssooidc v1.35.2 // indirect
	github.com/aws/aws-sdk-go-v2/service/sts v1.38.8 // indirect
	github.com/aws/smithy-go v1.23.1 // indirect
	github.com/beorn7/perks v1.0.1 // indirect
	github.com/bytedance/gopkg v0.1.3 // indirect
	github.com/cenkalti/backoff/v5 v5.0.3 // indirect
	github.com/clbanning/mxj v1.8.4 // indirect
	github.com/clipperhouse/uax29/v2 v2.2.0 // indirect
	github.com/containerd/cgroups v1.1.0 // indirect
	github.com/containerd/containerd/api v1.8.0 // indirect
	github.com/containerd/continuity v0.4.4 // indirect
	github.com/containerd/errdefs v1.0.0 // indirect
	github.com/containerd/fifo v1.1.0 // indirect
	github.com/containerd/log v0.1.0 // indirect
	github.com/containerd/platforms v0.2.1 // indirect
	github.com/containerd/ttrpc v1.2.7 // indirect
	github.com/containerd/typeurl/v2 v2.1.1 // indirect
	github.com/cyphar/filepath-securejoin v0.6.0 // indirect
	github.com/distribution/reference v0.6.0 // indirect
	github.com/docker/go-events v0.0.0-20190806004212-e31b211e4f1c // indirect
	github.com/emirpasic/gods v1.12.0 // indirect
	github.com/fasthttp/websocket v1.5.12
	github.com/felixge/httpsnoop v1.0.4 // indirect
	github.com/go-asn1-ber/asn1-ber v1.5.8-0.20250403174932-29230038a667 // indirect
	github.com/go-jose/go-jose/v4 v4.1.3 // indirect
	github.com/go-logr/logr v1.4.3 // indirect
	github.com/go-logr/stdr v1.2.2 // indirect
	github.com/go-viper/mapstructure/v2 v2.4.0 // indirect
	github.com/gogo/protobuf v1.3.2 // indirect
	github.com/golang/groupcache v0.0.0-20210331224755-41bb18bfe9da // indirect
	github.com/golang/mock v1.6.0 // indirect
	github.com/golang/protobuf v1.5.4 // indirect
	github.com/golang/snappy v1.0.0 // indirect
	github.com/google/go-cmp v0.7.0 // indirect
	github.com/google/go-querystring v1.1.0 // indirect
	github.com/google/s2a-go v0.1.7 // indirect
	github.com/googleapis/enterprise-certificate-proxy v0.3.6 // indirect
	github.com/googleapis/gax-go/v2 v2.12.0 // indirect
	github.com/grpc-ecosystem/grpc-gateway/v2 v2.27.3 // indirect
	github.com/hashicorp/go-immutable-radix v1.0.0 // indirect
	github.com/hashicorp/golang-lru v0.5.1 // indirect
	github.com/json-iterator/go v1.1.12 // indirect
	github.com/mattn/go-colorable v0.1.14 // indirect
	github.com/mattn/go-runewidth v0.0.19 // indirect
	github.com/minio/crc64nvme v1.1.1 // indirect
	github.com/moby/locker v1.0.1 // indirect
	github.com/moby/patternmatcher v0.6.0 // indirect
	github.com/moby/sys/mountinfo v0.7.1 // indirect
	github.com/moby/sys/sequential v0.6.0 // indirect
	github.com/moby/sys/signal v0.7.0 // indirect
	github.com/moby/sys/user v0.4.0 // indirect
	github.com/moby/sys/userns v0.1.0 // indirect
	github.com/modern-go/concurrent v0.0.0-20180306012644-bacd9c7ef1dd // indirect
	github.com/modern-go/reflect2 v1.0.3-0.20250322232337-35a7c28c31ee // indirect
	github.com/mozillazg/go-httpheader v0.4.0 // indirect
	github.com/munnerz/goautoneg v0.0.0-20191010083416-a7dc8b61c822 // indirect
	github.com/nicksnyder/go-i18n/v2 v2.4.1 // indirect
	github.com/opencontainers/go-digest v1.0.0 // indirect
	github.com/opencontainers/image-spec v1.1.1 // indirect
	github.com/opencontainers/selinux v1.13.0 // indirect
	github.com/patrickmn/go-cache v2.1.0+incompatible // indirect
	github.com/philhofer/fwd v1.2.0 // indirect
	github.com/pmezard/go-difflib v1.0.1-0.20181226105442-5d4384ee4fb2 // indirect
	github.com/prometheus/client_model v0.6.2 // indirect
	github.com/prometheus/common v0.66.1 // indirect
	github.com/prometheus/procfs v0.16.1 // indirect
	github.com/savsgio/gotils v0.0.0-20250924091648-bce9a52d7761 // indirect
	github.com/sirupsen/logrus v1.9.3 // indirect
	github.com/tidwall/gjson v1.13.0 // indirect
	github.com/tidwall/match v1.1.1 // indirect
	github.com/tidwall/pretty v1.2.0 // indirect
	github.com/tinylib/msgp v1.4.0 // indirect
	github.com/valyala/bytebufferpool v1.0.0 // indirect
	github.com/valyala/fasthttp v1.67.0
	go.opencensus.io v0.24.0 // indirect
	go.opentelemetry.io/auto/sdk v1.2.1 // indirect
	go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc v0.46.1 // indirect
	go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp v0.49.0 // indirect
	go.opentelemetry.io/otel/metric v1.39.0 // indirect
	go.opentelemetry.io/proto/otlp v1.9.0 // indirect
	go.uber.org/atomic v1.11.0 // indirect
	go.uber.org/multierr v1.11.0 // indirect
	go.yaml.in/yaml/v2 v2.4.2 // indirect
	go.yaml.in/yaml/v3 v3.0.4 // indirect
	golang.org/x/time v0.14.0 // indirect
	google.golang.org/genproto v0.0.0-20240116215550-a9fa1716bcac // indirect
	google.golang.org/genproto/googleapis/api v0.0.0-20251202230838-ff82c1b0f217 // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20251202230838-ff82c1b0f217 // indirect
	gopkg.in/yaml.v2 v2.4.0 // indirect
	stathat.com/c/consistent v1.0.0 // indirect
)

require (
	cloud.google.com/go/storage v1.37.0
	filippo.io/edwards25519 v1.1.1 // indirect
	github.com/aliyun/aliyun-oss-go-sdk v3.0.2+incompatible
	github.com/bytedance/sonic v1.14.1
	github.com/bytedance/sonic/loader v0.3.0 // indirect
	github.com/cespare/xxhash/v2 v2.3.0 // indirect
	github.com/cloudwego/base64x v0.1.6 // indirect
	github.com/coreos/go-oidc/v3 v3.16.0
	github.com/dgryski/go-rendezvous v0.0.0-20200823014737-9f7001d12a5f // indirect
	github.com/dustin/go-humanize v1.0.1 // indirect
	github.com/go-ini/ini v1.67.0 // indirect
	github.com/go-ldap/ldap/v3 v3.4.12
	github.com/go-sql-driver/mysql v1.9.3 // indirect
	github.com/goccy/go-json v0.10.5 // indirect
	github.com/inconshreveable/mousetrap v1.1.0 // indirect
	github.com/jinzhu/inflection v1.0.0 // indirect
	github.com/jinzhu/now v1.1.5 // indirect
	github.com/klauspost/compress v1.18.3 // indirect
	github.com/klauspost/cpuid/v2 v2.3.0 // indirect
	github.com/mattn/go-isatty v0.0.20 // indirect
	github.com/minio/md5-simd v1.1.2 // indirect
	github.com/mitchellh/mapstructure v1.5.0 // indirect
	github.com/pelletier/go-toml/v2 v2.2.4 // indirect
	github.com/pkg/errors v0.9.1
	github.com/rs/xid v1.6.0
	github.com/sagikazarmark/locafero v0.12.0 // indirect
	github.com/spf13/afero v1.15.0 // indirect
	github.com/spf13/cast v1.10.0 // indirect
	github.com/spf13/pflag v1.0.10 // indirect
	github.com/subosito/gotenv v1.6.0 // indirect
	github.com/tencentyun/cos-go-sdk-v5 v0.7.71
	github.com/twitchyliquid64/golang-asm v0.15.1 // indirect
	golang.org/x/arch v0.22.0 // indirect
	golang.org/x/sync v0.19.0
	golang.org/x/sys v0.40.0
	golang.org/x/text v0.33.0
	google.golang.org/protobuf v1.36.10
	gopkg.in/yaml.v3 v3.0.1 // indirect
	gorm.io/datatypes v1.2.7
)
