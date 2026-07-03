package artifact

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestGormStoreGetMissAndPut(t *testing.T) {
	store, db := newTestGormStore(t)

	ctx := context.Background()
	key := Key{Module: "madler/zlib", Version: "v1.3.1", MatrixStr: "amd64-linux"}
	want := Artifact{
		Source:   Source{Type: "ghcr", URL: "https://ghcr.io/v2/meteorsliu/llar/blobs/sha256:abc"},
		Type:     "tar.gz",
		Metadata: "-lz",
		Checksum: "abc",
	}

	if got, err := store.Get(ctx, key); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get miss = %+v, %v; want ErrNotFound", got, err)
	}

	inserted, err := store.Put(ctx, key, want)
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	if inserted != want {
		t.Fatalf("Put = %+v, want %+v", inserted, want)
	}

	got, err := store.Get(ctx, key)
	if err != nil {
		t.Fatalf("Get hit: %v", err)
	}
	if got != want {
		t.Fatalf("Get = %+v, want %+v", got, want)
	}

	assertPrimaryKey(t, db)
}

func TestGormStorePutReturnsExistingArtifactForSameKey(t *testing.T) {
	store, _ := newTestGormStore(t)

	ctx := context.Background()
	key := Key{Module: "madler/zlib", Version: "v1.3.1", MatrixStr: "amd64-linux"}
	first := Artifact{
		Source:   Source{Type: "ghcr", URL: "https://ghcr.io/v2/meteorsliu/llar/blobs/sha256:first"},
		Type:     "tar.gz",
		Metadata: "-lz",
		Checksum: "first",
	}
	second := Artifact{
		Source:   Source{Type: "ghcr", URL: "https://ghcr.io/v2/meteorsliu/llar/blobs/sha256:second"},
		Type:     "tar.gz",
		Metadata: "-lz-other",
		Checksum: "second",
	}

	if _, err := store.Put(ctx, key, first); err != nil {
		t.Fatalf("first Put: %v", err)
	}
	got, err := store.Put(ctx, key, second)
	if err != nil {
		t.Fatalf("second Put: %v", err)
	}
	if got != first {
		t.Fatalf("second Put = %+v, want canonical %+v", got, first)
	}
}

func TestGormStorePutReturnsCanonicalArtifactWithoutSelect(t *testing.T) {
	trace := &sqlTraceLogger{}
	store, _ := newTestGormStoreWithLogger(t, trace)

	ctx := context.Background()
	key := Key{Module: "madler/zlib", Version: "v1.3.1", MatrixStr: "amd64-linux"}
	first := Artifact{
		Source:   Source{Type: "ghcr", URL: "https://ghcr.io/v2/meteorsliu/llar/blobs/sha256:first"},
		Type:     "tar.gz",
		Metadata: "-lz",
		Checksum: "first",
	}
	second := Artifact{
		Source:   Source{Type: "ghcr", URL: "https://ghcr.io/v2/meteorsliu/llar/blobs/sha256:second"},
		Type:     "tar.gz",
		Metadata: "-lz-other",
		Checksum: "second",
	}

	if _, err := store.Put(ctx, key, first); err != nil {
		t.Fatalf("first Put: %v", err)
	}
	trace.Reset()

	got, err := store.Put(ctx, key, second)
	if err != nil {
		t.Fatalf("second Put: %v", err)
	}
	if got != first {
		t.Fatalf("second Put = %+v, want canonical %+v", got, first)
	}
	statements := trace.Statements()
	if len(statements) != 1 {
		t.Fatalf("statement count = %d, statements = %#v", len(statements), statements)
	}
	sql := strings.ToUpper(statements[0])
	for _, want := range []string{"INSERT", "ON CONFLICT", "RETURNING"} {
		if !strings.Contains(sql, want) {
			t.Fatalf("statement %q does not contain %q", statements[0], want)
		}
	}
	if strings.Contains(sql, "SELECT") {
		t.Fatalf("statement should not select separately: %q", statements[0])
	}
}

func TestGormStoreDelete(t *testing.T) {
	store, _ := newTestGormStore(t)

	ctx := context.Background()
	key := Key{Module: "madler/zlib", Version: "v1.3.1", MatrixStr: "amd64-linux"}
	value := Artifact{
		Source:   Source{Type: "ghcr", URL: "https://ghcr.io/v2/meteorsliu/llar/blobs/sha256:abc"},
		Type:     "tar.gz",
		Metadata: "-lz",
		Checksum: "abc",
	}

	if _, err := store.Put(ctx, key, value); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if err := store.Delete(ctx, key); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if got, err := store.Get(ctx, key); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get after Delete = %+v, %v; want ErrNotFound", got, err)
	}
}

func TestGormStoreDatabaseErrors(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("gorm.Open: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("db.DB: %v", err)
	}
	store, err := NewGormStore(db)
	if err != nil {
		t.Fatalf("NewGormStore: %v", err)
	}
	if err := sqlDB.Close(); err != nil {
		t.Fatalf("db.Close: %v", err)
	}

	ctx := context.Background()
	key := Key{Module: "madler/zlib", Version: "v1.3.1", MatrixStr: "amd64-linux"}
	value := Artifact{
		Source:   Source{Type: "ghcr", URL: "https://ghcr.io/v2/meteorsliu/llar/blobs/sha256:abc"},
		Type:     "tar.gz",
		Metadata: "-lz",
		Checksum: "abc",
	}
	if _, err := store.Get(ctx, key); err == nil {
		t.Fatal("Get with closed database = nil, want error")
	}
	if _, err := store.Put(ctx, key, value); err == nil {
		t.Fatal("Put with closed database = nil, want error")
	}
	if err := store.Delete(ctx, key); err == nil {
		t.Fatal("Delete with closed database = nil, want error")
	}
}

func newTestGormStore(t *testing.T) (*GormStore, *sql.DB) {
	t.Helper()
	return newTestGormStoreWithLogger(t, logger.Default.LogMode(logger.Silent))
}

func newTestGormStoreWithLogger(t *testing.T, log logger.Interface) (*GormStore, *sql.DB) {
	t.Helper()

	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{
		Logger: log,
	})
	if err != nil {
		t.Fatalf("gorm.Open: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("db.DB: %v", err)
	}
	t.Cleanup(func() {
		if err := sqlDB.Close(); err != nil {
			t.Fatalf("db.Close: %v", err)
		}
	})

	store, err := NewGormStore(db)
	if err != nil {
		t.Fatalf("NewGormStore: %v", err)
	}
	return store, sqlDB
}

type sqlTraceLogger struct {
	mu         sync.Mutex
	statements []string
}

func (l *sqlTraceLogger) LogMode(level logger.LogLevel) logger.Interface {
	return l
}

func (l *sqlTraceLogger) Info(ctx context.Context, msg string, args ...interface{}) {}

func (l *sqlTraceLogger) Warn(ctx context.Context, msg string, args ...interface{}) {}

func (l *sqlTraceLogger) Error(ctx context.Context, msg string, args ...interface{}) {}

func (l *sqlTraceLogger) Trace(ctx context.Context, begin time.Time, fc func() (sql string, rowsAffected int64), err error) {
	sql, _ := fc()
	l.mu.Lock()
	defer l.mu.Unlock()
	l.statements = append(l.statements, sql)
}

func (l *sqlTraceLogger) Reset() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.statements = nil
}

func (l *sqlTraceLogger) Statements() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.statements...)
}

func assertPrimaryKey(t *testing.T, db *sql.DB) {
	t.Helper()

	rows, err := db.Query(`PRAGMA table_info(artifacts)`)
	if err != nil {
		t.Fatalf("PRAGMA table_info: %v", err)
	}
	defer rows.Close()

	got := map[int]string{}
	for rows.Next() {
		var cid int
		var name string
		var typ string
		var notNull int
		var defaultValue sql.NullString
		var pk int
		if err := rows.Scan(&cid, &name, &typ, &notNull, &defaultValue, &pk); err != nil {
			t.Fatalf("PRAGMA scan: %v", err)
		}
		if pk != 0 {
			got[pk] = name
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("PRAGMA rows: %v", err)
	}

	want := map[int]string{1: "module", 2: "version", 3: "matrix_str"}
	if len(got) != len(want) {
		t.Fatalf("primary key = %+v, want %+v", got, want)
	}
	for pos, name := range want {
		if got[pos] != name {
			t.Fatalf("primary key position %d = %q, want %q", pos, got[pos], name)
		}
	}
}
