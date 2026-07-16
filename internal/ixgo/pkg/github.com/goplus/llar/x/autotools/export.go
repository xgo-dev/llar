// export by github.com/goplus/ixgo/cmd/qexp

package autotools

import (
	q "github.com/goplus/llar/x/autotools"

	"reflect"

	"github.com/goplus/ixgo"
)

func init() {
	ixgo.RegisterPackage(&ixgo.Package{
		Name: "autotools",
		Path: "github.com/goplus/llar/x/autotools",
		Deps: map[string]string{
			"github.com/goplus/llar/internal/execbroker": "execbroker",
			"os":            "os",
			"path/filepath": "filepath",
			"runtime":       "runtime",
		},
		Interfaces: map[string]reflect.Type{},
		NamedTypes: map[string]reflect.Type{
			"AutoTools": reflect.TypeOf((*q.AutoTools)(nil)).Elem(),
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
