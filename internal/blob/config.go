package blob

import (
	"fmt"
	"os"
	"strings"
)

const (
	envS3Endpoint  = "STELLA_BLOB_S3_ENDPOINT"
	envS3Bucket    = "STELLA_BLOB_S3_BUCKET"
	envS3AccessKey = "STELLA_BLOB_S3_ACCESS_KEY"
	envS3SecretKey = "STELLA_BLOB_S3_SECRET_KEY"
	envS3Region    = "STELLA_BLOB_S3_REGION"
	envS3UseSSL    = "STELLA_BLOB_S3_USE_SSL"
)

func NewStoreFromEnv() (Store, error) {
	vals := map[string]string{
		envS3Endpoint:  os.Getenv(envS3Endpoint),
		envS3Bucket:    os.Getenv(envS3Bucket),
		envS3AccessKey: os.Getenv(envS3AccessKey),
		envS3SecretKey: os.Getenv(envS3SecretKey),
	}
	allVals := map[string]string{
		envS3Endpoint:  vals[envS3Endpoint],
		envS3Bucket:    vals[envS3Bucket],
		envS3AccessKey: vals[envS3AccessKey],
		envS3SecretKey: vals[envS3SecretKey],
		envS3Region:    os.Getenv(envS3Region),
		envS3UseSSL:    os.Getenv(envS3UseSSL),
	}
	anySet := false
	for _, v := range allVals {
		if v != "" {
			anySet = true
			break
		}
	}
	if !anySet {
		return nil, nil
	}
	for _, v := range vals {
		if v == "" {
			return nil, fmt.Errorf("blob s3 config is partial; set %s, %s, %s, and %s together", envS3Endpoint, envS3Bucket, envS3AccessKey, envS3SecretKey)
		}
	}
	useSSL := true
	if raw := os.Getenv(envS3UseSSL); raw != "" {
		switch strings.ToLower(raw) {
		case "1", "true", "t", "yes", "y", "on":
			useSSL = true
		case "0", "false", "f", "no", "n", "off":
			useSSL = false
		default:
			return nil, fmt.Errorf("%s must be a boolean", envS3UseSSL)
		}
	}
	return NewS3Store(S3Config{
		Endpoint:  vals[envS3Endpoint],
		Bucket:    vals[envS3Bucket],
		AccessKey: vals[envS3AccessKey],
		SecretKey: vals[envS3SecretKey],
		Region:    os.Getenv(envS3Region),
		UseSSL:    useSSL,
	})
}
