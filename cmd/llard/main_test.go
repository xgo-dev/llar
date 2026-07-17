// Copyright (c) 2026 The XGo Authors (xgo.dev). All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import "testing"

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
	t.Setenv("LLARD_KODO_ACCESS_KEY", "")
	t.Setenv("LLARD_KODO_SECRET_KEY", "secret")
	t.Setenv("LLARD_KODO_BUCKET", "bucket")
	t.Setenv("LLARD_KODO_PUBLIC_DOMAIN", "https://example.com")

	if _, err := loadConfig(); err == nil || err.Error() != "LLARD_KODO_ACCESS_KEY is required" {
		t.Fatalf("loadConfig error = %v", err)
	}
}
