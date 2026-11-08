// release-assets-helper is a standalone CI caller, not release admission.
//
// pack reads INPUT/web/dist from a source workspace, writes a new absent
// DESTINATION/{manifest.json,web/dist/...}, reverifies it, and emits
// manifest_sha256=HEX only after successful descriptor cleanup. verify requires
// the expected digest supplied independently by the producer's job output.
// restore verifies an artifact and writes/reverifies a new absent dist directory.
// It never erases a checkout's tracked dist. The future workflow caller must
// arrange an invocation-owned absent destination without deleting user assets.
//
// install verifies an external artifact and fills only absent intended assets/
// leaves in an existing --workspace CHECKOUT/web/dist. Existing index.html,
// favicon.svg and other non-assets files must already match exactly; intended
// equal bundles are reused, never replaced. Full prewrite discovery rejects
// conflicting files, links, special entries and unexpected directories/files.
// Empty intended parent directories are allowed. verify-installed repeats the
// same complete admission with no creation and requires all files present.
// Both require --manifest-sha256 and forbid --destination. Forbidden flags are
// rejected even with empty values; original commands forbid --workspace while
// legacy verify continues to ignore --destination. Every invocation
// independently admits the external root; manifest rereads match both original
// canonical bytes and the trusted digest. Retained identities and final full
// source/destination revalidation reject observed replacements or changed bytes.
//
// Each command requires --input, --repository, --source-sha, --checkout-sha,
// --run-id and --run-attempt. pack/restore require --destination; verify/restore
// require --manifest-sha256. The caller must observe checkout SHA and validate
// producer success/artifact ID independently; this helper has no GitHub API,
// git subprocess, ambient environment or release authorization side effects.
// Manifest bytes are canonical helper-generated JSON without a trailing newline.
// Metadata must remain outside dist; inventory is bounded to 4096 files and
// 100 MiB including artifact metadata, with a 1 MiB manifest limit.
// Discovery also bounds directory entries, depth and retained roots. These are
// logical input limits, not a claim that process RSS equals the payload limit.
//
// Linux and macOS host support is implemented; this handoff was exercised only
// on Darwin/arm64, not Linux. Rooted directory and regular-file handles
// confine operations across directory moves; observed admission/destination
// replacements fail. Output directories and leaves are created exclusively,
// and already opened output parents remain held throughout copying. These are
// not whole-scaffold atomicity, crash durability or hostile-filesystem
// authenticity guarantees. There is an unavoidable interval between creating
// a directory and obtaining its handle. No native Windows assurance is claimed.
// Errors retain partial output rather than unlinking a path that a competitor
// may have replaced. Diagnostics report the requested destination, not a
// guaranteed current location: retained-parent moves can make the current
// pathname unknown. Descriptor cleanup failures are propagated.
// install closes all owned handles on both success and failure, retains primary
// and cleanup errors, and emits no digest before cleanup succeeds. It leaves
// partial intended assets in place; retries independently readmit exact complete
// leftovers and reject partial/conflicting ones without repair or deletion.
// Later compilers/checkers reopen paths: these guarantees are not race-free
// whole-build assurance. The integration caller must enforce exact tracked HEAD
// and clean source before and after install and preserve semantic/binary/azd
// admission. File equality here is not Git provenance; this helper invokes no
// Git, network or runtime to establish it.
//
// Content hashes establish integrity against caller-supplied source/run/digest
// identity, not trust in the workflow, reproducibility or genuine build
// execution. Layout checks reject common stub/incomplete payloads; they do not
// attest that Vite ran. Index checks cover quoted src/href references only;
// arbitrary JavaScript closure and browser route correctness require separately
// exercised production browser/embedded-server evidence.
package main
