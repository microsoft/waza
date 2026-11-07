package scripts

import (
	"os"
	"strings"
	"testing"
)

func TestManifestAggregateBrowserDiscovery(t *testing.T) {
	workflow, err := os.ReadFile("../workflows/web-ci.yml")
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"internal/manifestaggregate/**", "internal/webapi/aggregates.go", "schemas/manifest-aggregate-*.json"} {
		pattern := "- '" + path + "'"
		if strings.Count(string(workflow), pattern) != 2 {
			t.Errorf("aggregate input %s must select both main push and PR path-filter browser validation", path)
		}
	}
	if !strings.Contains(string(workflow), "npx --no-install playwright test --project=chromium --retries=0") {
		t.Fatal("dashboard gate must execute all Chromium specs without retries")
	}
	config, err := os.ReadFile("../../web/playwright.config.ts")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(config), `testDir: "./e2e"`) {
		t.Fatal("production Playwright discovery must include aggregate spec directory")
	}
	for _, path := range []string{"../../web/e2e/manifest-aggregate.spec.ts", "../../site/e2e/manifest-aggregation.spec.ts"} {
		info, err := os.Stat(path)
		if err != nil || !info.Mode().IsRegular() {
			t.Errorf("required aggregate browser spec not present: %s: %v", path, err)
		}
	}
}
