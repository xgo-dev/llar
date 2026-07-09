// export by github.com/goplus/ixgo/cmd/qexp

package formula

import (
	q "github.com/goplus/llar/formula"

	"go/constant"
	"reflect"

	"github.com/goplus/ixgo"
)

func init() {
	ixgo.RegisterPackage(&ixgo.Package{
		Name: "formula",
		Path: "github.com/goplus/llar/formula",
		Deps: map[string]string{
			"github.com/goplus/llar/mod/module": "module",
			"github.com/qiniu/x/gsh":            "gsh",
			"io/fs":                             "fs",
			"maps":                              "maps",
			"slices":                            "slices",
			"sort":                              "sort",
		},
		Interfaces: map[string]reflect.Type{},
		NamedTypes: map[string]reflect.Type{
			"BuildResult": reflect.TypeOf((*q.BuildResult)(nil)).Elem(),
			"Context":     reflect.TypeOf((*q.Context)(nil)).Elem(),
			"Matrix":      reflect.TypeOf((*q.Matrix)(nil)).Elem(),
			"ModuleDeps":  reflect.TypeOf((*q.ModuleDeps)(nil)).Elem(),
			"ModuleF":     reflect.TypeOf((*q.ModuleF)(nil)).Elem(),
			"Project":     reflect.TypeOf((*q.Project)(nil)).Elem(),
			"TestResult":  reflect.TypeOf((*q.TestResult)(nil)).Elem(),
		},
		AliasTypes: map[string]reflect.Type{},
		Vars:       map[string]reflect.Value{},
		Funcs: map[string]reflect.Value{
			"Gopt_ModuleF_Main": reflect.ValueOf(q.Gopt_ModuleF_Main),
			"NewContext":        reflect.ValueOf(q.NewContext),
		},
		TypedConsts: map[string]ixgo.TypedConst{},
		UntypedConsts: map[string]ixgo.UntypedConst{
			"GopPackage": {"untyped bool", constant.MakeBool(bool(q.GopPackage))},
		},
	})
}
