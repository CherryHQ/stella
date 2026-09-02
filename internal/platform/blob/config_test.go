package blob

import (
	"testing"

	"github.com/CherryHQ/stella/internal/platform/config"
)

func TestNewStoreFromConfig(t *testing.T) {
	// None set: no store, no error.
	if store, err := NewStoreFromConfig(config.BlobS3Config{}); err != nil || store != nil {
		t.Fatalf("all unset store=%T err=%v, want nil nil", store, err)
	}
	// Any core field set alone => partial-config error.
	if store, err := NewStoreFromConfig(config.BlobS3Config{Endpoint: "localhost:9000"}); err == nil || store != nil {
		t.Fatalf("partial store=%T err=%v, want error", store, err)
	}
	// A peripheral field alone (region) still trips the group constraint.
	if store, err := NewStoreFromConfig(config.BlobS3Config{Region: "us-east-1"}); err == nil || store != nil {
		t.Fatalf("region-only store=%T err=%v, want error", store, err)
	}
	// USE_SSL alone still requires the four core fields.
	if store, err := NewStoreFromConfig(config.BlobS3Config{UseSSL: "false"}); err == nil || store != nil {
		t.Fatalf("use-ssl-only store=%T err=%v, want error", store, err)
	}
	// An unrecognized USE_SSL value is rejected even with the core fields set —
	// the dialect is not narrowed to strconv.ParseBool.
	full := config.BlobS3Config{Endpoint: "localhost:9000", Bucket: "b", AccessKey: "a", SecretKey: "s"}
	bad := full
	bad.UseSSL = "maybe"
	if store, err := NewStoreFromConfig(bad); err == nil || store != nil {
		t.Fatalf("bad use-ssl store=%T err=%v, want error", store, err)
	}
	// A non-strconv truthy value (yes) is accepted by the dialect.
	ok := full
	ok.UseSSL = "yes"
	if _, err := NewStoreFromConfig(ok); err != nil {
		t.Fatalf("use-ssl=yes err=%v, want store built", err)
	}
}
