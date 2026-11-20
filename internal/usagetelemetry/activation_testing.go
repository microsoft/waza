package usagetelemetry

import "net/http"

// OverrideActivationForTesting replaces the build-time activation decisions so
// tests can drive a local fake collector. It is not reachable from any CLI
// flag, environment variable, or configuration file. The returned function
// restores the previous values. Overridden activation also admits development
// builds so tests need not stamp a release version.
func OverrideActivationForTesting(collector string, httpClient *http.Client, defaultOn, ciOn bool) (restore func()) {
	prevEndpoint, prevClient, prevDefault, prevCI, prevDev := endpoint, testHTTPClient, defaultCollectionOn, ciCollectionOn, devBuildCollectionOn
	endpoint, testHTTPClient, defaultCollectionOn, ciCollectionOn, devBuildCollectionOn = collector, httpClient, defaultOn, ciOn, true
	return func() {
		discardPending()
		endpoint, testHTTPClient, defaultCollectionOn, ciCollectionOn, devBuildCollectionOn = prevEndpoint, prevClient, prevDefault, prevCI, prevDev
	}
}
