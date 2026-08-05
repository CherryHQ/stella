package blob

import (
	"fmt"
	"strings"

	"github.com/CherryHQ/stella/internal/config"
)

// Env var names, retained for the group-constraint and dialect error messages.
// The values are loaded into config.BlobS3Config at the server boundary; this
// package owns only the validation and the S3 store construction.
const (
	envS3Endpoint  = "STELLA_BLOB_S3_ENDPOINT"
	envS3Bucket    = "STELLA_BLOB_S3_BUCKET"
	envS3AccessKey = "STELLA_BLOB_S3_ACCESS_KEY"
	envS3SecretKey = "STELLA_BLOB_S3_SECRET_KEY"
	envS3Region    = "STELLA_BLOB_S3_REGION"
	envS3UseSSL    = "STELLA_BLOB_S3_USE_SSL"
)

// ResolveS3Config validates the raw deployment-S3 settings shared by all blob
// backed domains. A nil result means this deployment uses local storage.
func ResolveS3Config(c config.BlobS3Config) (*S3Config, error) {
	if c.Endpoint == "" && c.Bucket == "" && c.AccessKey == "" &&
		c.SecretKey == "" && c.Region == "" && c.UseSSL == "" {
		return nil, nil
	}
	core := map[string]string{
		envS3Endpoint:  c.Endpoint,
		envS3Bucket:    c.Bucket,
		envS3AccessKey: c.AccessKey,
		envS3SecretKey: c.SecretKey,
	}
	for _, v := range core {
		if v == "" {
			return nil, fmt.Errorf("blob s3 config is partial; set %s, %s, %s, and %s together", envS3Endpoint, envS3Bucket, envS3AccessKey, envS3SecretKey)
		}
	}
	useSSL := true
	if raw := c.UseSSL; raw != "" {
		switch strings.ToLower(raw) {
		case "1", "true", "t", "yes", "y", "on":
			useSSL = true
		case "0", "false", "f", "no", "n", "off":
			useSSL = false
		default:
			return nil, fmt.Errorf("%s must be a boolean", envS3UseSSL)
		}
	}
	return &S3Config{
		Endpoint:  c.Endpoint,
		Bucket:    c.Bucket,
		AccessKey: c.AccessKey,
		SecretKey: c.SecretKey,
		Region:    c.Region,
		UseSSL:    useSSL,
	}, nil
}

// NewStoreFromConfig builds the mutable asset blob store from deployment S3.
// It preserves the historical nil result when no S3 group is configured.
func NewStoreFromConfig(c config.BlobS3Config) (Store, error) {
	cfg, err := ResolveS3Config(c)
	if err != nil || cfg == nil {
		return nil, err
	}
	return NewS3Store(*cfg)
}
