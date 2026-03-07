# Linus: PR #96 token limits precedence

Treat any non-empty `.waza.yaml` `tokens.limits` config as authoritative over legacy `.token-limits.json`, including overrides-only configs where `defaults` is omitted. The regression test covers the exact case with overrides-only YAML plus a legacy file present so future refactors don't accidentally reintroduce the fallback.
