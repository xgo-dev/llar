// export by github.com/goplus/ixgo/cmd/qexp

package module

import (
	q "github.com/goplus/llar/mod/module"

	"reflect"

	"github.com/goplus/ixgo"
)

func init() {
	ixgo.RegisterPackage(&ixgo.Package{
		Name: "module",
		Path: "github.com/goplus/llar/mod/module",
		Deps: map[string]string{
			"path/filepath": "filepath",
		},
		Interfaces: map[string]reflect.Type{},
		NamedTypes: map[string]reflect.Type{
			"Version": reflect.TypeOf((*q.Version)(nil)).Elem(),
		},
		AliasTypes: map[string]reflect.Type{},
		Vars:       map[string]reflect.Value{},
		Funcs: map[string]reflect.Value{
			"EscapePath": reflect.ValueOf(q.EscapePath),
		},
		TypedConsts:   map[string]ixgo.TypedConst{},
		UntypedConsts: map[string]ixgo.UntypedConst{},
	})
}
