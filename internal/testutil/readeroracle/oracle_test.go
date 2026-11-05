package readeroracle

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"testing"

	"github.com/stretchr/testify/require"
)

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
