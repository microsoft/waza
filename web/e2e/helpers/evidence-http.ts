import { createServer } from "node:http";

export async function startEvidenceHTTP(preview: string, digest: string, receipt: string, document: string, csv: string,
  qualified?: { receipt: string; csv: string; status?: number }) {
  const requests: string[] = [];
  const failures: string[] = [];
  // Chromium attachment requests bypass Playwright interception. Serve actual
  // same-origin HTTP responses and forward only the built frontend.
  const server = createServer((request, response) => {
    void (async () => {
      const path = new URL(request.url ?? "/", preview).pathname;
      if (path === `/api/v1/evidence/${digest}`) {
        response.writeHead(200, { "Content-Type": "application/json" });
        response.end(receipt);
        return;
      }
      if (qualified && path === `/api/v1/evidence/${digest}/categories`) {
        response.writeHead(qualified.status ?? 200, { "Content-Type": "application/json" });
        response.end(qualified.receipt);
        return;
      }
      const attachments: [string, string, string, string, string][] = [
        ["document", "json", "application/json", document, "execution-evidence"],
        ["csv", "csv", "text/csv; charset=utf-8", csv, "execution-evidence"],
      ];
      if (qualified) attachments.push(
          ["categories/document", "json", "application/json", qualified.receipt, "execution-evidence-categories"],
          ["categories/csv", "csv", "text/csv; charset=utf-8", qualified.csv, "execution-evidence-categories"],
      );
      for (const [suffix, extension, contentType, content, prefix] of attachments) {
        if (path !== `/api/v1/evidence/${digest}/${suffix}`) continue;
        requests.push(`${request.method} ${path}`);
        if (suffix.startsWith("categories/") && qualified && (qualified.status ?? 200) !== 200) {
          response.writeHead(qualified.status ?? 200, { "Content-Type": "application/json" });
          response.end(qualified.receipt);
          return;
        }
        response.writeHead(200, {
          "Content-Type": contentType,
          "Content-Disposition": `attachment; filename="${prefix}-${digest}.${extension}"`,
          "Content-Length": Buffer.byteLength(content),
        });
        response.end(content);
        return;
      }
      if (path.startsWith("/api/")) {
        response.writeHead(404);
        response.end("Fixture API route was not selected.");
        return;
      }
      const upstream = await fetch(`${preview}${path}`, { signal: AbortSignal.timeout(5_000) });
      const body = Buffer.from(await upstream.arrayBuffer());
      response.writeHead(upstream.status, { "Content-Type": upstream.headers.get("content-type") ?? "application/octet-stream" });
      response.end(body);
    })().catch((error: unknown) => {
      failures.push(error instanceof Error ? error.message : "Fixture HTTP response failed.");
      response.writeHead(500);
      response.end("Fixture HTTP response failed.");
    });
  });
  await new Promise<void>((resolve, reject) => {
    server.once("error", reject);
    server.listen(0, "127.0.0.1", resolve);
  });
  const address = server.address();
  if (address === null || typeof address === "string") {
    server.close();
    throw new Error("Fixture HTTP listener is unavailable.");
  }
  return {
    base: `http://127.0.0.1:${address.port}`, requests, failures,
    async close() {
      server.closeAllConnections();
      await new Promise<void>((resolve, reject) => server.close((error) => error ? reject(error) : resolve()));
    },
  };
}
