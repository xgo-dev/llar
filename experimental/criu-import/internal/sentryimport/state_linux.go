//go:build linux && arm64

package sentryimport

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/checkpoint-restore/go-criu/v8/crit/images/pstree"
	"golang.org/x/sys/unix"
	"gvisor.dev/gvisor/pkg/state"
	"gvisor.dev/gvisor/pkg/state/wire"
)

// These objects are the pinned gVisor state stream, not live kernel objects.
// Keeping the object IDs and references intact preserves aliasing while the
// namespace's ID indexes and cached numeric IDs are changed together.
type stateGraph struct {
	types   []*wire.Type
	ids     []wire.Uint
	objects map[wire.Uint]wire.Object
}

func readGraph(r *wire.Reader) (*stateGraph, error) {
	n, object, err := state.ReadHeader(r)
	if err != nil {
		return nil, err
	}
	if !object {
		return nil, fmt.Errorf("expected an object graph")
	}
	g := &stateGraph{objects: make(map[wire.Uint]wire.Object)}
	for uint64(len(g.ids)) < n {
		switch v := wire.Load(r).(type) {
		case *wire.Type:
			g.types = append(g.types, v)
		case wire.Uint:
			g.ids = append(g.ids, v)
			g.objects[v] = wire.Load(r)
		default:
			return nil, fmt.Errorf("unexpected state record %T", v)
		}
	}
	return g, nil
}

func (g *stateGraph) write(w *wire.Writer) error {
	if err := state.WriteHeader(w, uint64(len(g.ids)), true); err != nil {
		return err
	}
	for _, t := range g.types {
		wire.Save(w, t)
	}
	for _, id := range g.ids {
		wire.Save(w, id)
		wire.Save(w, g.objects[id])
	}
	return nil
}

func (g *stateGraph) field(s *wire.Struct, name string) *wire.Object {
	for i, field := range g.types[int(s.TypeID)-1].Fields {
		if field == name {
			return s.Field(i)
		}
	}
	panic(fmt.Sprintf("missing pinned state field %s.%s", g.types[int(s.TypeID)-1].Name, name))
}

func (g *stateGraph) resolve(o wire.Object) wire.Object {
	r, ok := o.(*wire.Ref)
	if !ok {
		return o
	}
	v := g.objects[r.Root]
	for i := len(r.Dots) - 1; i >= 0; i-- {
		switch d := r.Dots[i].(type) {
		case *wire.FieldName:
			v = *g.field(v.(*wire.Struct), string(*d))
		case wire.Index:
			v = v.(*wire.Array).Contents[int(d)]
		default:
			panic(fmt.Sprintf("unexpected reference traversal %T", d))
		}
	}
	return v
}

func (g *stateGraph) setNumber(slot *wire.Object, value int64) {
	switch o := (*slot).(type) {
	case *wire.Ref:
		if len(o.Dots) != 0 {
			panic("numeric field unexpectedly aliases another field")
		}
		target := g.objects[o.Root]
		g.setNumber(&target, value)
		g.objects[o.Root] = target
	case *wire.Struct:
		if o.Fields() != 1 {
			panic("expected scalar state wrapper")
		}
		g.setNumber(o.Field(0), value)
	case wire.Uint:
		*slot = wire.Uint(value)
	case wire.Int, wire.Nil:
		*slot = wire.Int(value)
	default:
		panic(fmt.Sprintf("unexpected numeric state %T", o))
	}
}

func stateNumber(o wire.Object) int64 {
	switch v := o.(type) {
	case wire.Int:
		return int64(v)
	case wire.Uint:
		return int64(v)
	case wire.Nil:
		return 0
	default:
		panic(fmt.Sprintf("expected numeric map entry, got %T", o))
	}
}

func (g *stateGraph) rewriteMap(slot *wire.Object, keys bool, ids map[int64]int64) {
	m, ok := g.resolve(*slot).(*wire.Map)
	if !ok {
		panic(fmt.Sprintf("expected ID index, got %T", g.resolve(*slot)))
	}
	values := m.Values
	if keys {
		values = m.Keys
	}
	for i := range values {
		old := stateNumber(values[i])
		value, ok := ids[old]
		if !ok {
			panic(fmt.Sprintf("unmapped template ID %d", old))
		}
		g.setNumber(&values[i], value)
	}
}

func rewriteIDs(input, output string, ids map[int64]int64, process *pstree.PstreeEntry) (err error) {
	defer func() {
		if p := recover(); p != nil {
			err = fmt.Errorf("rewriting pinned Sentry state: %v", p)
		}
	}()
	src, err := os.Open(input)
	if err != nil {
		return err
	}
	defer src.Close()
	dst, err := os.Create(output)
	if err != nil {
		return err
	}
	defer dst.Close()
	r := &wire.Reader{Reader: src}
	w := &wire.Writer{Writer: dst}
	cpu, err := readGraph(r)
	if err != nil {
		return err
	}
	if err := cpu.write(w); err != nil {
		return err
	}
	g, err := readGraph(r)
	if err != nil {
		return err
	}
	groups := map[int64]int64{1: int64(process.GetPid())}
	sessions := map[int64]int64{1: int64(process.GetSid())}
	pgroups := map[int64]int64{1: int64(process.GetPgid())}
	var mono, real unix.Timespec
	if err := unix.ClockGettime(unix.CLOCK_MONOTONIC, &mono); err != nil {
		return err
	}
	if err := unix.ClockGettime(unix.CLOCK_REALTIME, &real); err != nil {
		return err
	}
	var maxID int64
	for _, id := range ids {
		if id > maxID {
			maxID = id
		}
	}
	var walk func(wire.Object)
	walk = func(o wire.Object) {
		switch v := o.(type) {
		case *wire.Struct:
			name := ""
			if v.TypeID != 0 {
				name = g.types[int(v.TypeID)-1].Name
			}
			switch name {
			case "pkg/sentry/kernel.PIDNamespace":
				g.setNumber(g.field(v, "last"), maxID)
				g.rewriteMap(g.field(v, "tasks"), true, ids)
				g.rewriteMap(g.field(v, "tids"), false, ids)
				g.rewriteMap(g.field(v, "tgids"), false, groups)
				g.rewriteMap(g.field(v, "sessions"), true, sessions)
				g.rewriteMap(g.field(v, "sids"), false, sessions)
				g.rewriteMap(g.field(v, "processGroups"), true, pgroups)
				g.rewriteMap(g.field(v, "pgids"), false, pgroups)
			case "pkg/sentry/kernel.threadGroupNode":
				g.setNumber(g.field(v, "pidWithinNS"), int64(process.GetPid()))
			case "pkg/sentry/kernel.Session":
				g.setNumber(g.field(v, "id"), int64(process.GetSid()))
			case "pkg/sentry/kernel.ProcessGroup":
				g.setNumber(g.field(v, "id"), int64(process.GetPgid()))
			case "pkg/sentry/kernel.Timekeeper":
				g.setNumber(g.field(v, "saveMonotonic"), mono.Nano())
				g.setNumber(g.field(v, "saveRealtime"), real.Nano())
				g.setNumber(g.field(v, "bootTime"), real.Nano()-mono.Nano())
			}
			for i := 0; i < v.Fields(); i++ {
				walk(*v.Field(i))
			}
		case *wire.Array:
			for _, item := range v.Contents {
				walk(item)
			}
		case *wire.Map:
			for _, item := range v.Keys {
				walk(item)
			}
			for _, item := range v.Values {
				walk(item)
			}
		case *wire.Interface:
			walk(v.Value)
		}
	}
	for _, id := range g.ids {
		walk(g.objects[id])
	}
	for _, typ := range g.types {
		if strings.Contains(typ.Name, "PIDNamespace") {
			fmt.Fprintf(os.Stderr, "rewrote %s IDs %v\n", typ.Name, ids)
		}
	}
	if err := g.write(w); err != nil {
		return err
	}
	_, err = io.Copy(dst, src) // MemoryFile metadata and page payloads are unchanged.
	return err
}
