package scripts

import (
	"os"
	"os/exec"
	"testing"
)

// The native package checker is its own command package; Go CI selects
// ./.github/scripts, so run its contract and mutation tests from here.
func TestNativePackageSmokeChecker(t *testing.T) {
	cmd := exec.Command("go", "test", "-count=1", "./native-package-smoke")
	cmd.Env = os.Environ()
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("native package smoke checker tests failed: %v\n%s", err, out)
	}
}
