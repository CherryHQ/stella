// Package blob stores opaque bytes behind one interface, backed by
// S3-compatible object storage. Callers address content by key and never learn
// which backend answered. Tests that need a real Store without an S3 server use
// the filesystem-backed one in blob/blobtest.
package blob
