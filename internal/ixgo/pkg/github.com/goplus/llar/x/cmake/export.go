// export by github.com/goplus/ixgo/cmd/qexp

package cmake

import (
	q "github.com/goplus/llar/x/cmake"

	"reflect"

	"github.com/goplus/ixgo"
)

func init() {
	ixgo.RegisterPackage(&ixgo.Package{
		Name: "cmake",
		Path: "github.com/goplus/llar/x/cmake",
		Deps: map[string]string{
			"github.com/goplus/llar/internal/execbroker": "execbroker",
			"os":            "os",
			"path/filepath": "filepath",
			"runtime":       "runtime",
			"sort":          "sort",
		},
		Interfaces: map[string]reflect.Type{},
		NamedTypes: map[string]reflect.Type{
			"CMake": reflect.TypeOf((*q.CMake)(nil)).Elem(),
		},
		AliasTypes: map[string]reflect.Type{},
		Vars:       map[string]reflect.Value{},
		Funcs: map[string]reflect.Value{
			"New": reflect.ValueOf(q.New),
		},
		TypedConsts:   map[string]ixgo.TypedConst{},
		UntypedConsts: map[string]ixgo.UntypedConst{},
	})
}
