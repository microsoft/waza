package commandmock

import (
	"reflect"
	"testing"
)

func TestSanitizeArgsRedactsOnlySensitiveFlagValue(t *testing.T) {
	got := sanitizeArgs(
		[]string{"--client-secret", "secret-value", "group", "show"},
		[]string{"AZURE_CLIENT_SECRET=secret-value"},
	)
	want := []string{redactedArg, redactedArg, "group", "show"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("sanitizeArgs() = %#v, want %#v", got, want)
	}
}
