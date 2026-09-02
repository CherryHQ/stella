// Package blob stores opaque bytes behind one interface, backed either by the
// local filesystem or by S3-compatible object storage. Callers address content
// by key and never learn which backend answered.
package blob
