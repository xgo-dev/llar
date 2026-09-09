package c

import (
	"reflect"
	"testing"
)

func TestToolchain(t *testing.T) {
	toolchain := NewToolchain([]string{"cc", "--target=aarch64-linux-gnu"}, []string{"c++", "--target=aarch64-linux-gnu"}, []string{"ld.lld"}, "ar", "ranlib", "nm", "strip")
	for _, test := range []struct {
		name string
		got  []string
		want []string
	}{
		{name: "CC", got: toolchain.CC(), want: []string{"cc", "--target=aarch64-linux-gnu"}},
		{name: "CXX", got: toolchain.CXX(), want: []string{"c++", "--target=aarch64-linux-gnu"}},
		{name: "Linker", got: toolchain.Linker(), want: []string{"ld.lld"}},
	} {
		if !reflect.DeepEqual(test.got, test.want) {
			t.Fatalf("%s = %q, want %q", test.name, test.got, test.want)
		}
	}
	for _, test := range []struct {
		name string
		got  string
		want string
	}{
		{name: "Archiver", got: toolchain.Archiver(), want: "ar"},
		{name: "Ranlib", got: toolchain.Ranlib(), want: "ranlib"},
		{name: "NM", got: toolchain.NM(), want: "nm"},
		{name: "Strip", got: toolchain.Strip(), want: "strip"},
	} {
		if test.got != test.want {
			t.Fatalf("%s = %q, want %q", test.name, test.got, test.want)
		}
	}
}
