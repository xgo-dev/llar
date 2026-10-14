// export by github.com/goplus/ixgo/cmd/qexp

package gnu

import (
	q "github.com/goplus/llar/build/runtime/x/gnu"

	"reflect"

	"github.com/goplus/ixgo"
)

func init() {
	ixgo.RegisterPackage(&ixgo.Package{
		Name:       "gnu",
		Path:       "github.com/goplus/llar/build/runtime/x/gnu",
		Deps:       map[string]string{},
		Interfaces: map[string]reflect.Type{},
		NamedTypes: map[string]reflect.Type{},
		AliasTypes: map[string]reflect.Type{},
		Vars:       map[string]reflect.Value{},
		Funcs: map[string]reflect.Value{
			"Compare": reflect.ValueOf(q.Compare),
		},
		TypedConsts:   map[string]ixgo.TypedConst{},
		UntypedConsts: map[string]ixgo.UntypedConst{},
	})
}
