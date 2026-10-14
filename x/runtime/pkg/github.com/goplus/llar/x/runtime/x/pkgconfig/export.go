// export by github.com/goplus/ixgo/cmd/qexp

package pkgconfig

import (
	q "github.com/goplus/llar/x/runtime/x/pkgconfig"

	"reflect"

	"github.com/goplus/ixgo"
)

func init() {
	ixgo.RegisterPackage(&ixgo.Package{
		Name: "pkgconfig",
		Path: "github.com/goplus/llar/x/runtime/x/pkgconfig",
		Deps: map[string]string{
			"fmt": "fmt",
			"github.com/goplus/llar/internal/execbroker": "execbroker",
			"io":            "io",
			"maps":          "maps",
			"os":            "os",
			"path/filepath": "filepath",
			"slices":        "slices",
			"sort":          "sort",
			"strings":       "strings",
		},
		Interfaces: map[string]reflect.Type{},
		NamedTypes: map[string]reflect.Type{
			"File": reflect.TypeOf((*q.File)(nil)).Elem(),
			"Spec": reflect.TypeOf((*q.Spec)(nil)).Elem(),
		},
		AliasTypes: map[string]reflect.Type{},
		Vars:       map[string]reflect.Value{},
		Funcs: map[string]reflect.Value{
			"CFlags":     reflect.ValueOf(q.CFlags),
			"Libs":       reflect.ValueOf(q.Libs),
			"Lookup":     reflect.ValueOf(q.Lookup),
			"New":        reflect.ValueOf(q.New),
			"StaticLibs": reflect.ValueOf(q.StaticLibs),
			"Use":        reflect.ValueOf(q.Use),
		},
		TypedConsts:   map[string]ixgo.TypedConst{},
		UntypedConsts: map[string]ixgo.UntypedConst{},
	})
}
