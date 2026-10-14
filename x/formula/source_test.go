package formula

import (
	"go/token"
	"io/fs"
	"os"
	"testing"

	"github.com/goplus/ixgo/xgobuild"
	"github.com/goplus/xgo/ast"
	"github.com/goplus/xgo/parser"
)

func TestMetadataOf(t *testing.T) {
	tests := []struct {
		name    string
		source  string
		want    formulaMetadata
		wantErr bool
	}{
		{
			name:   "literal metadata",
			source: "id \"test/pkg\"\nfromVer \"1.2.3\"\n",
			want:   formulaMetadata{modPath: "test/pkg", fromVer: "1.2.3"},
		},
		{
			name:   "backtick metadata",
			source: "id `test/pkg`\nfromVer `2.0.0`\n",
			want:   formulaMetadata{modPath: "test/pkg", fromVer: "2.0.0"},
		},
		{
			name: "hooks",
			source: `id "test/pkg"
fromVer "1.0.0"
onRequire (proj, deps) => {}
onBuild ctx => { fromVer "not metadata" }
onTest ctx => {}
filter => { return true }
`,
			want: formulaMetadata{modPath: "test/pkg", fromVer: "1.0.0", onRequire: true, onBuild: true, onTest: true, filter: true},
		},
		{name: "missing version", source: "id \"test/pkg\"", wantErr: true},
		{name: "missing id", source: "fromVer \"1.0.0\"", wantErr: true},
		{name: "empty source", wantErr: true},
		{name: "empty id", source: "id \"\"\nfromVer \"1.0.0\"", wantErr: true},
		{name: "dynamic version", source: "id \"test/pkg\"\nfromVer version", wantErr: true},
		{name: "syntax error", source: "!!!", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := metadataOf("test_llar.gox", []byte(tt.source))
			if (err != nil) != tt.wantErr {
				t.Fatalf("metadataOf error = %v, wantErr %v", err, tt.wantErr)
			}
			if !tt.wantErr && got != tt.want {
				t.Fatalf("metadata = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestParseCallArg(t *testing.T) {
	tests := []struct {
		name    string
		source  string
		fnName  string
		want    string
		wantErr bool
	}{
		{
			name:   "string literal with double quotes",
			source: `fromVer "1.0.0"`,
			fnName: "fromVer",
			want:   "1.0.0",
		},
		{
			name:   "string literal with backticks",
			source: "fromVer `2.0.0`",
			fnName: "fromVer",
			want:   "2.0.0",
		},
		{
			name:    "empty argument",
			source:  `fromVer ""`,
			fnName:  "fromVer",
			want:    "",
			wantErr: true,
		},
		{
			name:   "id function call",
			source: `id "test/pkg"`,
			fnName: "id",
			want:   "test/pkg",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fset := token.NewFileSet()
			astFile, err := parser.ParseEntry(fset, "test_llar.gox", []byte(tt.source), parser.Config{
				ClassKind: xgobuild.ClassKind,
			})
			if err != nil {
				t.Fatalf("failed to parse source: %v", err)
			}

			var callExpr *ast.CallExpr
			ast.Inspect(astFile, func(n ast.Node) bool {
				if c, ok := n.(*ast.CallExpr); ok {
					if ident, ok := c.Fun.(*ast.Ident); ok && ident.Name == tt.fnName {
						callExpr = c
						return false
					}
				}
				return true
			})

			if callExpr == nil {
				t.Fatalf("failed to find %s call in AST", tt.fnName)
			}

			got, err := parseCallArg(callExpr, tt.fnName)
			if (err != nil) != tt.wantErr {
				t.Errorf("parseCallArg() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if got != tt.want {
				t.Errorf("parseCallArg() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestParseCallArg_NoArgument(t *testing.T) {
	callExpr := &ast.CallExpr{
		Fun:  &ast.Ident{Name: "testFunc"},
		Args: []ast.Expr{},
	}

	_, err := parseCallArg(callExpr, "testFunc")
	if err == nil {
		t.Error("parseCallArg() expected error for no arguments")
	}
}

func TestParseCallArg_NonStringArg(t *testing.T) {
	callExpr := &ast.CallExpr{
		Fun: &ast.Ident{Name: "testFunc"},
		Args: []ast.Expr{
			&ast.Ident{Name: "someVariable"},
		},
	}

	_, err := parseCallArg(callExpr, "testFunc")
	if err == nil {
		t.Error("parseCallArg() expected error for non-string argument")
	}
}

func TestFromVerOf(t *testing.T) {
	fsys := os.DirFS("../modules/testdata/DaveGamble/cJSON").(fs.ReadFileFS)

	tests := []struct {
		name        string
		path        string
		wantFromVer string
		wantErr     bool
	}{
		{
			name:        "cJSON 1.0.0",
			path:        "1.0.0/CJSON_llar.gox",
			wantFromVer: "v1.0.0",
		},
		{
			name:        "cJSON 1.5.0",
			path:        "1.5.0/CJSON_llar.gox",
			wantFromVer: "v1.5.0",
		},
		{
			name:        "cJSON 2.0.0",
			path:        "2.0.0/CJSON_llar.gox",
			wantFromVer: "v2.0.0",
		},
		{
			name:    "nonexistent file",
			path:    "nonexistent/formula_llar.gox",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f, err := loadFS(fsys, tt.path)
			var got string
			if f != nil {
				got = f.FromVer
			}
			if (err != nil) != tt.wantErr {
				t.Errorf("fromVerOf() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if got != tt.wantFromVer {
				t.Errorf("fromVerOf() = %q, want %q", got, tt.wantFromVer)
			}
		})
	}
}

func TestFromVerFrom(t *testing.T) {
	tests := []struct {
		name        string
		source      string
		wantFromVer string
		wantErr     bool
	}{
		{
			name: "valid fromVer call",
			source: `
id "test/pkg"
fromVer "1.2.3"
`,
			wantFromVer: "1.2.3",
		},
		{
			name:        "fromVer with backticks",
			source:      "id `test/pkg`\nfromVer `2.0.0`\n",
			wantFromVer: "2.0.0",
		},
		{
			name: "no fromVer call",
			source: `
id "test/pkg"
onBuild (ctx, proj, out) => {
    echo "hello"
}
`,
			wantErr: true,
		},
		{
			name:    "empty source",
			source:  "",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fset := token.NewFileSet()
			astFile, err := parser.ParseEntry(fset, "test_llar.gox", []byte(tt.source), parser.Config{
				ClassKind: xgobuild.ClassKind,
			})
			if err != nil {
				t.Fatalf("failed to parse source: %v", err)
			}

			metadata, err := metadataFrom(astFile)
			got := metadata.fromVer
			if (err != nil) != tt.wantErr {
				t.Errorf("fromVerFrom() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if got != tt.wantFromVer {
				t.Errorf("fromVerFrom() = %q, want %q", got, tt.wantFromVer)
			}
		})
	}
}
