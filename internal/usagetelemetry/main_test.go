package usagetelemetry

import (
	"os"
	"testing"
)

var shippedEndpoint, shippedDefaultOn, shippedCIOn = endpoint, defaultCollectionOn, ciCollectionOn

// TestMain makes the package inert so no test can reach the real collector;
// tests opt in to a local fake collector with activate.
func TestMain(m *testing.M) {
	OverrideActivationForTesting("", nil, false, false)
	os.Exit(m.Run())
}
