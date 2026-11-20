package readeroracle

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRunCurrentProcess(t *testing.T) {
	type observation struct {
		Value string
		PID   int
	}
	if raw, subprocess := Input(t); subprocess {
		var value string
		require.NoError(t, json.Unmarshal(raw, &value))
		Output(t, observation{Value: value, PID: os.Getpid()})
		return
	}
	executable, err := os.Executable()
	require.NoError(t, err)
	raw := Run(t, executable, "TestRunCurrentProcess", "current reader")
	var got observation
	require.NoError(t, json.Unmarshal(raw, &got))
	require.Equal(t, "current reader", got.Value)
	require.NotEqual(t, os.Getpid(), got.PID)
	if testing.CoverMode() != "" {
		directory := flag.Lookup("test.gocoverdir").Value.String()
		require.NotEmpty(t, directory, "run covered regression through go test")
		counters, err := filepath.Glob(filepath.Join(directory, fmt.Sprintf("covcounters.*.%d.*", got.PID)))
		require.NoError(t, err)
		require.Len(t, counters, 1, "current reader coverage must reach the parent's exported profile")
	}
}

func TestDescribe(t *testing.T) {
	require.Nil(t, Describe(nil))
	syntax := &json.SyntaxError{Offset: 7}
	described := Describe(syntax)
	require.Equal(t, "*json.SyntaxError", described.Type)
	require.Equal(t, int64(7), described.Offset)
	typeError := &json.UnmarshalTypeError{Value: "number", Type: reflect.TypeFor[string](),
		Offset: 23, Struct: "wire", Field: "kind"}
	require.Equal(t, &Error{Type: "*json.UnmarshalTypeError", Message: typeError.Error(),
		Value: "number", Target: "string", Offset: 23, Struct: "wire", Field: "kind"}, Describe(typeError))
	wrapped := Describe(fmt.Errorf("reader: %w", context.Canceled))
	require.True(t, wrapped.Canceled)
	require.False(t, wrapped.Deadline)
	require.Len(t, wrapped.Causes, 1)
	require.True(t, wrapped.Causes[0].Canceled)
	joined := Describe(errors.Join(typeError, context.DeadlineExceeded))
	require.True(t, joined.Deadline)
	require.False(t, joined.Canceled)
	require.Len(t, joined.Causes, 2)
	require.Equal(t, *Describe(typeError), joined.Causes[0])
	require.Equal(t, *Describe(context.DeadlineExceeded), joined.Causes[1])
}
