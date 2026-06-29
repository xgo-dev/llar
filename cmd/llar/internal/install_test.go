package internal

import "testing"

func TestRunInstallPanicsUntilImplemented(t *testing.T) {
	defer func() {
		got := recover()
		if got != "TODO" {
			t.Fatalf("runInstall panic = %v, want TODO", got)
		}
	}()
	_ = runInstall(nil, []string{"madler/zlib@v1.3.1"})
}
