// Package rootedfile shares the platform-specific rooted read-only opener.
// Open returns an owned raw descriptor, not a validated regular-file snapshot.
// Callers must check the opened file and enforce their own read bounds and
// cancellation. Unix implementations request O_NONBLOCK; the Windows precheck
// does not establish nonblocking or race-free opening. This is not hostile
// filesystem authenticity.
package rootedfile
