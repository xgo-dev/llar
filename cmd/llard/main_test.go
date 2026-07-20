// Copyright (c) 2026 The XGo Authors (xgo.dev). All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"os"
	"strings"
	"testing"
)

func TestLoadConfig(t *testing.T) {
	t.Setenv("LLARD_ADDR", "127.0.0.1:9000")
	t.Setenv("LLARD_KODO_ACCESS_KEY", "access")
	t.Setenv("LLARD_KODO_SECRET_KEY", "secret")
	t.Setenv("LLARD_KODO_BUCKET", "bucket")
	t.Setenv("LLARD_KODO_PUBLIC_DOMAIN", "https://example.com")
	t.Setenv("LLARD_KODO_PREFIX", "artifacts")

	cfg, err := loadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.addr != "127.0.0.1:9000" {
		t.Fatalf("addr = %q", cfg.addr)
	}
	if cfg.accessKey != "access" || cfg.secretKey != "secret" || cfg.bucket != "bucket" {
		t.Fatalf("unexpected Kodo credentials: %#v", cfg)
	}
	if cfg.publicDomain != "https://example.com" || cfg.prefix != "artifacts" {
		t.Fatalf("unexpected Kodo location: %#v", cfg)
	}
}

func TestLoadConfigDefaultAddr(t *testing.T) {
	t.Setenv("LLARD_ADDR", "")
	t.Setenv("LLARD_KODO_ACCESS_KEY", "access")
	t.Setenv("LLARD_KODO_SECRET_KEY", "secret")
	t.Setenv("LLARD_KODO_BUCKET", "bucket")
	t.Setenv("LLARD_KODO_PUBLIC_DOMAIN", "https://example.com")

	cfg, err := loadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.addr != ":8080" {
		t.Fatalf("addr = %q, want :8080", cfg.addr)
	}
}

func TestLoadConfigRequiresKodoSettings(t *testing.T) {
	tests := []struct {
		name string
		env  string
		err  string
	}{
		{name: "access key", env: "LLARD_KODO_ACCESS_KEY", err: "LLARD_KODO_ACCESS_KEY is required"},
		{name: "secret key", env: "LLARD_KODO_SECRET_KEY", err: "LLARD_KODO_SECRET_KEY is required"},
		{name: "bucket", env: "LLARD_KODO_BUCKET", err: "LLARD_KODO_BUCKET is required"},
		{name: "public domain", env: "LLARD_KODO_PUBLIC_DOMAIN", err: "LLARD_KODO_PUBLIC_DOMAIN is required"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("LLARD_KODO_ACCESS_KEY", "access")
			t.Setenv("LLARD_KODO_SECRET_KEY", "secret")
			t.Setenv("LLARD_KODO_BUCKET", "bucket")
			t.Setenv("LLARD_KODO_PUBLIC_DOMAIN", "https://example.com")
			t.Setenv(tt.env, "")

			if _, err := loadConfig(); err == nil || err.Error() != tt.err {
				t.Fatalf("loadConfig error = %v, want %q", err, tt.err)
			}
		})
	}
}

func TestRunRejectsInvalidDotEnv(t *testing.T) {
	t.Chdir(t.TempDir())
	if err := os.WriteFile(".env", []byte("INVALID='unterminated\n"), 0600); err != nil {
		t.Fatal(err)
	}

	err := run()
	if err == nil || !strings.HasPrefix(err.Error(), "load .env: ") {
		t.Fatalf("run error = %v, want .env load error", err)
	}
}

func TestRunRequiresConfig(t *testing.T) {
	t.Chdir(t.TempDir())
	t.Setenv("LLARD_KODO_ACCESS_KEY", "")
	t.Setenv("LLARD_KODO_SECRET_KEY", "")
	t.Setenv("LLARD_KODO_BUCKET", "")
	t.Setenv("LLARD_KODO_PUBLIC_DOMAIN", "")

	if err := run(); err == nil || err.Error() != "LLARD_KODO_ACCESS_KEY is required" {
		t.Fatalf("run error = %v", err)
	}
}

func TestRunRejectsInvalidAddress(t *testing.T) {
	t.Chdir(t.TempDir())
	cacheDir := t.TempDir()
	t.Setenv("HOME", cacheDir)
	t.Setenv("XDG_CACHE_HOME", cacheDir)
	t.Setenv("LLARD_ADDR", "127.0.0.1:not-a-port")
	t.Setenv("LLARD_KODO_ACCESS_KEY", "access")
	t.Setenv("LLARD_KODO_SECRET_KEY", "secret")
	t.Setenv("LLARD_KODO_BUCKET", "bucket")
	t.Setenv("LLARD_KODO_PUBLIC_DOMAIN", "https://example.com")

	if err := run(); err == nil || !strings.Contains(err.Error(), "not-a-port") {
		t.Fatalf("run error = %v, want listen error for not-a-port", err)
	}
}
