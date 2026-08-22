// Package otelenv answers one question from the standard OpenTelemetry
// environment variables: is this signal being exported?
//
// It exists so that question has exactly one answer in the process. The
// tracer-provider setup and the HTTP transport that emits spans have to agree:
// when they disagreed, a traces-specific endpoint produced parent spans with
// no children, and OTEL_TRACES_EXPORTER=none silenced export while the
// transport kept paying for spans nobody collected. The variables are owned by
// the OTel spec, not by stella, which is why this reads them directly rather
// than mirroring them onto server config.
package otelenv

import "os"

// TracesEnabled reports whether span export is configured and not switched off.
func TracesEnabled() bool {
	return SignalEnabled("OTEL_TRACES_EXPORTER", "OTEL_EXPORTER_OTLP_TRACES_ENDPOINT")
}

// LogsEnabled reports whether log export is configured and not switched off.
func LogsEnabled() bool {
	return SignalEnabled("OTEL_LOGS_EXPORTER", "OTEL_EXPORTER_OTLP_LOGS_ENDPOINT")
}

// MetricsEnabled reports whether metric export is configured and not switched off.
func MetricsEnabled() bool {
	return SignalEnabled("OTEL_METRICS_EXPORTER", "OTEL_EXPORTER_OTLP_METRICS_ENDPOINT")
}

// SignalEnabled reports whether one signal has somewhere to export to — an
// OTLP endpoint, generic or signal-specific, or an explicit exporter such as
// "console" — and the operator has not opted out. OTEL_SDK_DISABLED=true and
// <SIGNAL>_EXPORTER=none are the standard kill switches; either silences the
// signal even when an endpoint is present.
func SignalEnabled(exporterVar, endpointVar string) bool {
	if os.Getenv("OTEL_SDK_DISABLED") == "true" {
		return false
	}
	exporter := os.Getenv(exporterVar)
	if exporter == "none" {
		return false
	}
	return os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT") != "" || os.Getenv(endpointVar) != "" || exporter != ""
}
