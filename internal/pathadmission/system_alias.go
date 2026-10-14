package pathadmission

import (
	"fmt"
	"os"
	"runtime"
	"strings"
)

// SystemRootAlias preserves the two literal Darwin root aliases without
// resolving user-selected ancestors or granting directory admission.
func SystemRootAlias(absolute string) (string, error) {
	if runtime.GOOS != "darwin" {
		return absolute, nil
	}
	return systemRootAlias(absolute, os.Readlink)
}

func systemRootAlias(absolute string, readlink func(string) (string, error)) (string, error) {
	for _, alias := range []string{"/var", "/tmp"} {
		if absolute != alias && !strings.HasPrefix(absolute, alias+"/") {
			continue
		}
		target, err := readlink(alias)
		if err != nil {
			return "", fmt.Errorf("checking system root alias %s: %w", alias, err)
		}
		if target != "/private"+alias && target != "private"+alias {
			return "", fmt.Errorf("system root alias %s has an unexpected target; use its physical path", alias)
		}
		return "/private" + absolute, nil
	}
	return absolute, nil
}
