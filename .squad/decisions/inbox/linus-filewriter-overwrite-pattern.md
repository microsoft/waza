# Decision: FileWriter overwrite pattern uses delete-then-create

**By:** Linus (Backend Developer)
**Date:** 2026-02-26
**Issue:** #58

## What

When a file needs to be overwritten (e.g., malformed SKILL.md repair), the caller should `os.Remove` the file before passing it to `FileWriter`. The FileWriter then sees it as absent and creates it normally with ➕ indicator. This avoids adding overwrite/force-write complexity to the shared `FileWriter` API.

## Why

The FileWriter's single responsibility is create-if-missing + skip-if-exists. Adding an overwrite flag would complicate the API and make it harder to reason about. The delete-then-create pattern keeps the FileWriter simple while letting callers handle their own pre-write logic.

## Impact

Any future command that needs to overwrite an existing file via FileWriter should follow this pattern: detect the condition, remove the stale file, then call `fw.Write()`.
