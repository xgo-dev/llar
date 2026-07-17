package metadata

import (
	"reflect"
	"strings"
	"testing"

	"github.com/goplus/llar/mod/module"
)

func TestEncodeDecode(t *testing.T) {
	buildDir := "/tmp/llard/foo@1.0.0-amd64-linux"
	installDir := "/home/user/.llar/foo@1.0.0-amd64-linux"
	value := "-L" + buildDir + "/lib -Wl,-rpath," + buildDir + "/lib -lz"
	deps := []module.Version{{Path: "madler/zlib", Version: "v1.3.1"}}

	data, err := Encode(value, buildDir, deps)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), buildDir) {
		t.Fatalf("encoded metadata contains build directory: %s", data)
	}
	if !strings.Contains(string(data), `{{.InstallDir}}`) {
		t.Fatalf("encoded metadata does not contain install template: %s", data)
	}

	got, gotDeps, err := Decode(data, installDir)
	if err != nil {
		t.Fatal(err)
	}
	want := "-L" + installDir + "/lib -Wl,-rpath," + installDir + "/lib -lz"
	if got != want {
		t.Fatalf("Decode() metadata = %q, want %q", got, want)
	}
	if !reflect.DeepEqual(gotDeps, deps) {
		t.Fatalf("Decode() deps = %+v, want %+v", gotDeps, deps)
	}
}

func TestDecodeErrors(t *testing.T) {
	tests := []struct {
		name string
		data string
	}{
		{name: "invalid json", data: "{"},
		{name: "missing metadata", data: `{}`},
		{name: "invalid template", data: `{"metadata":"{{"}`},
		{name: "invalid dependency", data: `{"metadata":"-lz","deps":["madler/zlib"]}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, _, err := Decode([]byte(tt.data), t.TempDir()); err == nil {
				t.Fatal("Decode() error = nil")
			}
		})
	}
}
