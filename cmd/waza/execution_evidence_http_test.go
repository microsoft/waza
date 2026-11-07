package main

import (
	"context"
	"os/exec"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestExecutionEvidenceHTTPDownloadFixtureServesExactProducerBytes(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, "node", "--input-type=module", "-e", `
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { createServer } from "node:http";
import { startEvidenceHTTP } from "../../web/e2e/helpers/evidence-http.ts";
const receipt = readFileSync("../../web/e2e/fixtures/execution-evidence.json", "utf8");
const csv = readFileSync("../../web/e2e/fixtures/execution-evidence.csv", "utf8");
const document = receipt.slice(receipt.indexOf('"document":') + '"document":'.length, receipt.indexOf(',"sources":'));
const digest = JSON.parse(receipt).evidence_sha256;
const categoryResults = readFileSync("../../web/e2e/fixtures/execution-evidence-category-results.json", "utf8");
const qualifiedCSV = readFileSync("../../web/e2e/fixtures/execution-evidence-qualified.csv", "utf8");
const qualifiedReceipt = receipt.trim().slice(0, -1) + ',"categories":' + categoryResults.trim() + "}";
const frontend = createServer((request, response) => {
  assert.equal(request.url, "/");
  response.writeHead(200, {"Content-Type": "text/html"});
  response.end("<html>actual bounded frontend fixture</html>");
});
await new Promise(resolve => frontend.listen(0, "127.0.0.1", resolve));
let fixture;
try {
  fixture = await startEvidenceHTTP("http://127.0.0.1:" + frontend.address().port, digest, receipt, document, csv,
    {receipt: qualifiedReceipt, csv: qualifiedCSV});
  assert.equal(await (await fetch(fixture.base + "/")).text(), "<html>actual bounded frontend fixture</html>");
  assert.equal(await (await fetch(fixture.base + "/api/v1/evidence/" + digest)).text(), receipt);
  for (const [suffix, extension, body, type] of [
    ["document", "json", document, "application/json"], ["csv", "csv", csv, "text/csv; charset=utf-8"],
  ]) {
    const response = await fetch(fixture.base + "/api/v1/evidence/" + digest + "/" + suffix);
    assert.equal(response.status, 200);
    assert.equal(response.headers.get("content-type"), type);
    assert.equal(response.headers.get("content-disposition"), 'attachment; filename="execution-evidence-' + digest + '.' + extension + '"');
    assert.equal(response.headers.get("content-length"), String(Buffer.byteLength(body)));
    assert.equal(await response.text(), body);
    assert.ok(body.includes("9007199254740993"));
  }
  assert.equal(await (await fetch(fixture.base + "/api/v1/evidence/" + digest + "/categories")).text(), qualifiedReceipt);
  for (const [suffix, extension, body, type] of [
    ["document", "json", qualifiedReceipt, "application/json"], ["csv", "csv", qualifiedCSV, "text/csv; charset=utf-8"],
  ]) {
    const response = await fetch(fixture.base + "/api/v1/evidence/" + digest + "/categories/" + suffix);
    assert.equal(response.status, 200);
    assert.equal(response.headers.get("content-type"), type);
    assert.equal(response.headers.get("content-disposition"), 'attachment; filename="execution-evidence-categories-' + digest + '.' + extension + '"');
    assert.equal(response.headers.get("content-length"), String(Buffer.byteLength(body)));
    assert.equal(await response.text(), body);
  }
  assert.equal((await fetch(fixture.base + "/api/unknown")).status, 404);
  assert.deepEqual(fixture.requests, [
    "GET /api/v1/evidence/" + digest + "/document", "GET /api/v1/evidence/" + digest + "/csv",
    "GET /api/v1/evidence/" + digest + "/categories/document", "GET /api/v1/evidence/" + digest + "/categories/csv",
  ]);
  assert.deepEqual(fixture.failures, []);
} finally {
  try {
    await fixture?.close();
  } finally {
    frontend.closeAllConnections();
    await new Promise((resolve, reject) => frontend.close(error => error ? reject(error) : resolve()));
  }
}
`).CombinedOutput()
	require.NoError(t, err, string(output))
}
