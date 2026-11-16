package usagetelemetry

import "net/http"

// OverrideActivationForTesting replaces the build-time activation decisions so
// tests can drive a local fake collector. It is not reachable from any CLI
// flag, environment variable, or configuration file. The returned function
// restores the previous values.
func OverrideActivationForTesting(collector string, httpClient *http.Client, defaultOn, ciOn bool) (restore func()) {
	prevEndpoint, prevClient, prevDefault, prevCI := endpoint, testHTTPClient, defaultCollectionOn, ciCollectionOn
	endpoint, testHTTPClient, defaultCollectionOn, ciCollectionOn = collector, httpClient, defaultOn, ciOn
	return func() {
		discardPending()
		endpoint, testHTTPClient, defaultCollectionOn, ciCollectionOn = prevEndpoint, prevClient, prevDefault, prevCI
	}
}
