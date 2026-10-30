package runnercontract

import (
	"encoding/json"
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestExactInteger(t *testing.T) {
	maxInt := int(^uint(0) >> 1)
	minInt := -maxInt - 1
	for _, tc := range []struct {
		token string
		want  int
	}{
		{"6e4", 60000}, {"2.0", 2}, {"20e-1", 2},
		{"0.000", 0}, {"-0e12", 0}, {"-20e-1", -2},
		{"2.0000000000000000000000000000000000000000", 2},
		{strconv.Itoa(maxInt) + ".0", maxInt},
		{strconv.Itoa(minInt) + "0e-1", minInt},
	} {
		t.Run(tc.token, func(t *testing.T) {
			got, err := exactInteger(json.Number(tc.token))
			require.NoError(t, err)
			require.Equal(t, tc.want, got)
		})
	}
	for _, value := range []any{
		nil, "2", true, 2, 2.0, []any{}, map[string]any{},
		json.Number("invalid"), json.Number("2.0000000000000001"),
		json.Number("21e-1"), json.Number("1e100"),
		json.Number(strconv.FormatUint(uint64(maxInt)+1, 10)),
		json.Number("-" + strconv.FormatUint(uint64(maxInt)+2, 10)),
	} {
		_, err := exactInteger(value)
		require.Error(t, err, "%v", value)
	}
}
