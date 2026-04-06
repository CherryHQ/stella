package trace

import (
	"os"
	"strings"
)

// config holds OTel exporter settings, populated from standard OTel env vars.
type config struct {
	Endpoint    string  // OTLP gRPC endpoint (empty = OTel disabled)
	ServiceName string  // OTel service name
	Insecure    bool    // use insecure gRPC connection
	SampleRate  float64 // trace sampling rate [0.0, 1.0]
}

func loadConfig() config {
	endpoint := os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT")

	// Normalize: the gRPC exporter's WithEndpoint expects host:port,
	// but users may pass a URL with scheme per the OTel spec.
	endpoint = strings.TrimPrefix(endpoint, "http://")
	endpoint = strings.TrimPrefix(endpoint, "https://")

	cfg := config{
		Endpoint:    endpoint,
		ServiceName: "anna",
		Insecure:    true,
		SampleRate:  1.0,
	}
	if v := os.Getenv("OTEL_SERVICE_NAME"); v != "" {
		cfg.ServiceName = v
	}
	if os.Getenv("OTEL_EXPORTER_OTLP_INSECURE") == "false" {
		cfg.Insecure = false
	}
	return cfg
}
