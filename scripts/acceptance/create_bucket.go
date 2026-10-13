//go:build ignore

// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0
// This explicit fixture utility never targets a production endpoint.
package main

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"os"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run() error {
	if os.Getenv("ARCENTRA_REAL_STACK") != "1" {
		return fmt.Errorf("explicit ARCENTRA_REAL_STACK=1 is required")
	}
	u, err := url.Parse(os.Getenv("ARCENTRA_TEST_S3_ENDPOINT"))
	if err != nil || u.Scheme != "http" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "" {
		return fmt.Errorf("loopback fixture S3 endpoint required")
	}
	ip := net.ParseIP(u.Hostname())
	if ip == nil || !ip.IsLoopback() {
		return fmt.Errorf("numeric loopback fixture S3 endpoint required")
	}
	access, secret := os.Getenv("ARCENTRA_TEST_S3_ACCESS_KEY"), os.Getenv("ARCENTRA_TEST_S3_SECRET_KEY")
	if access == "" || secret == "" {
		return fmt.Errorf("synthetic S3 credentials required")
	}
	client, err := minio.New(u.Host, &minio.Options{Creds: credentials.NewStaticV4(access, secret, ""), Secure: false})
	if err != nil {
		return fmt.Errorf("fixture S3 configuration failed")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	exists, err := client.BucketExists(ctx, "acceptance")
	if err != nil {
		return fmt.Errorf("fixture S3 readiness failed")
	}
	if exists {
		return nil
	}
	if err := client.MakeBucket(ctx, "acceptance", minio.MakeBucketOptions{}); err != nil {
		return fmt.Errorf("fixture S3 bucket creation failed")
	}
	return nil
}
