package assurance

import (
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

// Only expected fixture metadata is shared. The test executable is immutable in
// this process; replacement experiments run in their own executing-copy process.
// Production observations must still acquire fresh bytes on every invocation.
var fixtureExecutableSHA256 = sync.OnceValues(CurrentExecutableSHA256)

func TestFixtureExecutableIdentityMatchesFreshObservation(t *testing.T) {
	expected, err := fixtureExecutableSHA256()
	require.NoError(t, err)
	observed, err := CurrentExecutableSHA256()
	require.NoError(t, err)
	require.Equal(t, observed, expected)
}
