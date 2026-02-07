package env

import (
	"os"
	"strconv"
	"strings"
	"time"
)

func GetEnvInt(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if value, err := strconv.Atoi(v); err == nil {
			return value
		}
	}
	return def
}

func GetEnvBool(key string, def bool) bool {
	if v := os.Getenv(key); v != "" {
		if value, err := strconv.ParseBool(v); err == nil {
			return value
		}
	}
	return def
}

func GetEnvFloat64(key string, def float64) float64 {
	if v := os.Getenv(key); v != "" {
		if value, err := strconv.ParseFloat(v, 64); err == nil {
			return value
		}
	}
	return def
}

func GetEnvDuration(key string, def time.Duration) time.Duration {
	if v := os.Getenv(key); v != "" {
		if value, err := time.ParseDuration(v); err == nil {
			return value
		}
	}
	return def
}

func GetEnvString(key string, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func GetEnvStringSlice(key string, def []string) []string {
	if v := os.Getenv(key); v != "" {
		return strings.Split(v, ",")
	}
	return def
}

func GetEnvStringMap(key string, def map[string]string) map[string]string {
	if v := os.Getenv(key); v != "" {
		parts := strings.Split(v, ",")
		m := make(map[string]string)
		for _, part := range parts {
			kv := strings.SplitN(part, "=", 2)
			if len(kv) == 2 {
				m[kv[0]] = kv[1]
			}
		}
		return m
	}
	return def
}

func GetEnvStringMapSlice(key string, def map[string][]string) map[string][]string {
	if v := os.Getenv(key); v != "" {
		parts := strings.Split(v, ",")
		m := make(map[string][]string)
		for _, part := range parts {
			kv := strings.SplitN(part, "=", 2)
			if len(kv) == 2 {
				m[kv[0]] = strings.Split(kv[1], ",")
			}
		}
		return m
	}
	return def
}
