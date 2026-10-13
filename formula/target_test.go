package formula

import (
	"io/fs"
	"os"
	"slices"
	"testing"

	classfile "github.com/goplus/llar/runtime/formula"
)

func TestSetMatrix(t *testing.T) {
	for _, instance := range []string{"loaded metadata", "clone"} {
		for _, hook := range []string{"onRequire", "onBuild", "onTest", "filter"} {
			t.Run(instance+"/"+hook, func(t *testing.T) {
				path := "targetsurface_llar.gox"
				if hook == "onTest" {
					path = "hello_llar.gox"
				}
				source, err := loadFS(os.DirFS("testdata/formula").(fs.ReadFileFS), path)
				if err != nil {
					t.Fatal(err)
				}
				f := source
				if instance == "clone" {
					f = Clone(source)
				}
				matrix := classfile.Matrix{
					Require:        map[string][]string{"os": {"linux"}},
					Options:        map[string][]string{"debug": {"ON"}},
					DefaultOptions: map[string][]string{"callerDefault": {"ignored"}},
				}
				f.SetMatrix(matrix)
				if source.program != nil || f.program != nil || f.structElem.IsValid() {
					t.Fatal("SetMatrix initialized the formula before a hook call")
				}
				matrix.Require["os"][0] = "windows"
				matrix.Options["debug"][0] = "OFF"
				matrix.Options["extra"] = []string{"ignored"}

				switch hook {
				case "onRequire":
					var deps classfile.ModuleDeps
					f.OnRequire(&classfile.Project{}, &deps)
					if got := deps.Deps(); len(got) != 1 || got[0].Path != "madler/zlib" {
						t.Fatalf("OnRequire did not see default zlib=ON: %+v", got)
					}
				case "onBuild":
					f.OnBuild(&classfile.Context{})
				case "onTest":
					f.OnTest(&classfile.Context{})
				case "filter":
					if !f.Filter() {
						t.Fatal("Filter did not see default zlib=ON")
					}
				}
				target := valueOf(f.structElem, "target").(classfile.Matrix)
				if !slices.Equal(target.Require["os"], []string{"linux"}) ||
					!slices.Equal(target.Options["debug"], []string{"ON"}) {
					t.Fatalf("selected matrix was not isolated from input mutations: %+v", target)
				}
				if _, ok := target.Options["extra"]; ok {
					t.Fatal("formula retained the caller's options map")
				}
				if _, ok := target.DefaultOptions["callerDefault"]; ok {
					t.Fatal("caller replaced formula-owned defaults")
				}
				if instance == "clone" {
					if source.program != f.program {
						t.Fatal("clone did not reuse the source program")
					}
					sourceTarget := valueOf(source.structElem, "target").(classfile.Matrix)
					if len(sourceTarget.Require) != 0 || len(sourceTarget.Options["debug"]) != 0 {
						t.Fatal("clone's matrix changed the source instance")
					}
				}

				if hook != "onTest" {
					f.SetMatrix(classfile.Matrix{Options: map[string][]string{"zlib": {"OFF"}}})
					if f.Filter() {
						t.Fatal("SetMatrix did not override zlib after initialization")
					}
				}
				f.SetMatrix(classfile.Matrix{})
				target = valueOf(f.structElem, "target").(classfile.Matrix)
				if len(target.Require) != 0 || len(target.Options["debug"]) != 0 {
					t.Fatalf("reset retained previous selections: %+v", target)
				}
				if hook != "onTest" && !f.Filter() {
					t.Fatal("reset did not restore formula defaults")
				}
			})
		}
	}
}
