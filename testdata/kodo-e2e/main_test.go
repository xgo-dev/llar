package main

import (
	"os"
	"testing"
)

func TestKodoE2E(t *testing.T) {
	if os.Getenv("QINIU_ACCESS_KEY") == "" || os.Getenv("QINIU_SECRET_KEY") == "" || os.Getenv("QINIU_BUCKET") == "" {
		t.Skip("QINIU_ACCESS_KEY, QINIU_SECRET_KEY, and QINIU_BUCKET are required")
	}
	cfg := config{
		accessKey:     os.Getenv("QINIU_ACCESS_KEY"),
		secretKey:     os.Getenv("QINIU_SECRET_KEY"),
		bucket:        os.Getenv("QINIU_BUCKET"),
		publicDomain:  envOrDefault("QINIU_PUBLIC_DOMAIN", defaultPublicDomain),
		prefix:        os.Getenv("QINIU_PREFIX"),
		formulaRoot:   "formulas",
		target:        defaultTarget,
		sharedTargets: defaultSharedTargets,
		matrix:        hostMatrix(),
		timeout:       defaultTimeout,
	}
	if err := cfg.validate(); err != nil {
		t.Fatal(err)
	}
	if err := run(cfg); err != nil {
		t.Fatal(err)
	}
}
