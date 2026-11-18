//go:build !unix

package provenance

// Non-Unix platforms have no FIFOs inside bundle directories to block on.
const openNonblock = 0
