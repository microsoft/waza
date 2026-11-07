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
// Each command requires --input, --repository, --source-sha, --checkout-sha,
// --run-id and --run-attempt. pack/restore require --destination; verify/restore
// require --manifest-sha256. The caller must observe checkout SHA and validate
// producer success/artifact ID independently; this helper has no GitHub API,
// git subprocess, ambient environment or release authorization side effects.
// Manifest bytes are canonical helper-generated JSON without a trailing newline.
// Metadata must remain outside dist; inventory is bounded to 4096 files and
// 100 MiB including artifact metadata, with a 1 MiB manifest limit.
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
//
// Content hashes establish integrity against caller-supplied source/run/digest
// identity, not trust in the workflow, reproducibility or genuine build
// execution. Layout checks reject common stub/incomplete payloads; they do not
// attest that Vite ran. Index checks cover quoted src/href references only;
// arbitrary JavaScript closure and browser route correctness require separately
// exercised production browser/embedded-server evidence.
package main
