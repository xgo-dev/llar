package modules

import (
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/goplus/llar/mod/module"
)

// testStruct is used for testing reflection utilities
type testStruct struct {
	exportedField   string
	unexportedField int
	anotherField    bool
}

func TestUnexportValueOf(t *testing.T) {
	ts := testStruct{
		exportedField:   "hello",
		unexportedField: 42,
		anotherField:    true,
	}

	val := reflect.ValueOf(&ts).Elem()

	tests := []struct {
		name      string
		fieldName string
		wantValue any
	}{
		{
			name:      "unexported int field",
			fieldName: "unexportedField",
			wantValue: 42,
		},
		{
			name:      "unexported bool field",
			fieldName: "anotherField",
			wantValue: true,
		},
		{
			name:      "exported string field",
			fieldName: "exportedField",
			wantValue: "hello",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			field := val.FieldByName(tt.fieldName)
			result := unexportValueOf(field)

			if !result.IsValid() {
				t.Fatal("unexportValueOf returned invalid value")
			}

			got := result.Interface()
			if got != tt.wantValue {
				t.Errorf("unexportValueOf() = %v, want %v", got, tt.wantValue)
			}
		})
	}
}

func TestValueOf(t *testing.T) {
	ts := testStruct{
		exportedField:   "world",
		unexportedField: 100,
		anotherField:    false,
	}

	val := reflect.ValueOf(&ts).Elem()

	tests := []struct {
		name      string
		fieldName string
		wantValue any
	}{
		{
			name:      "get unexported int",
			fieldName: "unexportedField",
			wantValue: 100,
		},
		{
			name:      "get unexported bool",
			fieldName: "anotherField",
			wantValue: false,
		},
		{
			name:      "get exported string",
			fieldName: "exportedField",
			wantValue: "world",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := valueOf(val, tt.fieldName)
			if got != tt.wantValue {
				t.Errorf("valueOf() = %v, want %v", got, tt.wantValue)
			}
		})
	}
}

// testStructWithFunc is used for testing function field extraction
type testStructWithFunc struct {
	callback func(a, b int) int
}

func TestValueOf_FunctionField(t *testing.T) {
	addFunc := func(a, b int) int { return a + b }
	ts := testStructWithFunc{
		callback: addFunc,
	}

	val := reflect.ValueOf(&ts).Elem()
	got := valueOf(val, "callback")

	fn, ok := got.(func(int, int) int)
	if !ok {
		t.Fatalf("valueOf() returned %T, want func(int, int) int", got)
	}

	result := fn(3, 4)
	if result != 7 {
		t.Errorf("callback(3, 4) = %d, want 7", result)
	}
}

func TestLoadComparator_InvalidPath(t *testing.T) {
	// Test with non-existent file
	_, err := loadComparatorFS(os.DirFS("/nonexistent").(fs.ReadFileFS), "/path/cmp.gox")
	if err == nil {
		t.Error("loadComparator should return error for non-existent file")
	}
}

func TestLoadComparator_Fake(t *testing.T) {
	// Test with non-existent file
	cmp, err := loadComparatorFS(os.DirFS("testdata").(fs.ReadFileFS), "fakecomp/fakecomp_cmp.gox")
	if err != nil {
		t.Error("loadComparator should return error for non-existent file")
	}
	if cmp(module.Version{"a", "v1"}, module.Version{"b", "v1"}) != -1 {
		t.Error("unexpected result")
	}
}

func TestLoadComparator_OutsideModule(t *testing.T) {
	formulaDir, err := filepath.Abs("testdata/DaveGamble/cJSON")
	if err != nil {
		t.Fatal(err)
	}

	// Formula repositories do not contain go.mod. For example, llarhub stores
	// CJSON_cmp.gox beside versions.json, so loading it must not invoke go list.
	t.Chdir(t.TempDir())
	comp, err := loadComparatorFS(os.DirFS(formulaDir).(fs.ReadFileFS), "CJSON_cmp.gox")
	if err != nil {
		t.Fatalf("loadComparatorFS outside a module: %v", err)
	}
	if got := comp(module.Version{Version: "v1.0.0"}, module.Version{Version: "v2.0.0"}); got >= 0 {
		t.Fatalf("comparison = %d, want a negative value", got)
	}
}

func TestLoadComparator_InvalidFileExtension(t *testing.T) {
	// Create a temp file with wrong extension
	tempDir := t.TempDir()
	invalidFile := tempDir + "/test.txt"

	// loadComparator expects .gox files, so this should fail
	_, err := loadComparatorFS(os.DirFS("testdata").(fs.ReadFileFS), invalidFile)
	if err == nil {
		t.Error("loadComparator should return error for invalid file")
	}
}

func TestLoadComparator_EmptyDirectory(t *testing.T) {
	tempDir := t.TempDir()

	// Try to load from empty directory
	_, err := loadComparatorFS(os.DirFS("testdata").(fs.ReadFileFS), tempDir)
	if err == nil {
		t.Error("loadComparator should return error for directory path")
	}
}

func TestLoadComparator_WithRealTestData(t *testing.T) {
	// Use the real testdata comparator file
	cmpPath := "DaveGamble/cJSON/CJSON_cmp.gox"

	comp, err := loadComparatorFS(os.DirFS("testdata").(fs.ReadFileFS), cmpPath)
	if err != nil {
		t.Fatalf("loadComparatorFS(), %q) error = %v", cmpPath, err)
	}

	if comp == nil {
		t.Fatal("loadComparator returned nil comparator")
	}

	// Test that the comparator works correctly with semver versions
	tests := []struct {
		name string
		v1   module.Version
		v2   module.Version
		want int // -1, 0, or 1
	}{
		{
			name: "v1 < v2",
			v1:   module.Version{Path: "test", Version: "v1.0.0"},
			v2:   module.Version{Path: "test", Version: "v2.0.0"},
			want: -1,
		},
		{
			name: "v1 > v2",
			v1:   module.Version{Path: "test", Version: "v2.0.0"},
			v2:   module.Version{Path: "test", Version: "v1.0.0"},
			want: 1,
		},
		{
			name: "v1 == v2",
			v1:   module.Version{Path: "test", Version: "v1.0.0"},
			v2:   module.Version{Path: "test", Version: "v1.0.0"},
			want: 0,
		},
		{
			name: "patch version comparison",
			v1:   module.Version{Path: "test", Version: "v1.0.1"},
			v2:   module.Version{Path: "test", Version: "v1.0.2"},
			want: -1,
		},
		{
			name: "minor version comparison",
			v1:   module.Version{Path: "test", Version: "v1.2.0"},
			v2:   module.Version{Path: "test", Version: "v1.1.0"},
			want: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := comp(tt.v1, tt.v2)
			// semver.Compare returns -1, 0, or 1
			if got != tt.want {
				t.Errorf("comp(%v, %v) = %d, want %d", tt.v1, tt.v2, got, tt.want)
			}
		})
	}
}
