//go:build linux && arm64

package sentryimport

import (
	"fmt"

	"gvisor.dev/gvisor/pkg/state/wire"
)

func (g *stateGraph) object(o wire.Object) wire.Object {
	for {
		switch v := o.(type) {
		case *wire.Ref:
			if v.Root == 0 {
				return nil
			}
			o = g.resolve(v)
		case *wire.Interface:
			o = v.Value
		default:
			return o
		}
	}
}

func (g *stateGraph) number(o wire.Object) int64 {
	o = g.object(o)
	if s, ok := o.(*wire.Struct); ok && s.Fields() == 1 {
		return g.number(*s.Field(0))
	}
	return stateNumber(o)
}

func (g *stateGraph) boolean(o wire.Object) bool {
	switch v := g.object(o).(type) {
	case wire.Bool:
		return bool(v)
	case wire.Nil:
		return false
	default:
		panic(fmt.Sprintf("expected bool, got %T", v))
	}
}

func (g *stateGraph) items(o wire.Object) []wire.Object {
	switch v := g.object(o).(type) {
	case *wire.Array:
		return v.Contents
	case *wire.Slice:
		return g.items(&v.Ref)[:int(v.Length)]
	case nil:
		return nil
	default:
		panic(fmt.Sprintf("expected array/slice, got %T", v))
	}
}

func (g *stateGraph) structures(name string) []*wire.Struct {
	var found []*wire.Struct
	var walk func(wire.Object)
	walk = func(o wire.Object) {
		switch v := o.(type) {
		case *wire.Struct:
			if v.TypeID != 0 && g.types[int(v.TypeID)-1].Name == name {
				found = append(found, v)
			}
			for i := 0; i < v.Fields(); i++ {
				walk(*v.Field(i))
			}
		case *wire.Array:
			for _, e := range v.Contents {
				walk(e)
			}
		case *wire.Map:
			for _, e := range v.Keys {
				walk(e)
			}
			for _, e := range v.Values {
				walk(e)
			}
		case *wire.Interface:
			walk(v.Value)
		}
	}
	for _, id := range g.ids {
		walk(g.objects[id])
	}
	return found
}

func (g *stateGraph) taskStates() map[int64]*wire.Struct {
	out := make(map[int64]*wire.Struct)
	namespaces := g.structures("pkg/sentry/kernel.PIDNamespace")
	if len(namespaces) != 1 {
		panic("export requires one PID namespace")
	}
	index := g.object(*g.field(namespaces[0], "tasks")).(*wire.Map)
	for i, key := range index.Keys {
		out[g.number(key)] = g.object(index.Values[i]).(*wire.Struct)
	}
	return out
}

func (g *stateGraph) fdImplementations(task *wire.Struct) map[int64]*wire.Struct {
	table := g.object(*g.field(task, "fdTable")).(*wire.Struct)
	index := g.object(*g.field(table, "descriptorTable")).(*wire.Map)
	out := make(map[int64]*wire.Struct)
	for i, key := range index.Keys {
		descriptor := g.object(index.Values[i]).(*wire.Struct)
		file := g.object(*g.field(descriptor, "file")).(*wire.Struct)
		out[g.number(key)] = g.object(*g.field(file, "impl")).(*wire.Struct)
	}
	return out
}
