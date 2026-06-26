// Package mlruntime resolves and installs the native ML sidecar runtime — the
// stella-ml binary, libonnxruntime, and the models — mirroring internal/pgruntime.
// The bundle is downloaded at runtime (not embedded) and split into two
// independently versioned artifacts: a runtime bundle (binary + onnxruntime) and a
// model bundle, so swapping models does not re-download the libraries.
//
// Only darwin and linux are supported; windows has no prebuilt tokenizer library.
package mlruntime

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
)

const (
	// RuntimeVersion versions the binary + onnxruntime bundle. ModelVersion versions
	// the model bundle. The runtime pins the model version it was tested against.
	RuntimeVersion = "ort1.27.0-e5"
	ModelVersion   = "e5-small-int8-v1"

	// DefaultRuntimeRepo hosts the release artifacts (does not exist yet; the
	// download path is wired ahead of the release pipeline).
	DefaultRuntimeRepo = "CherryHQ/stella-ml-runtime"

	// Dev overrides point the resolver at a locally built sidecar + models instead
	// of a downloaded bundle, so local development needs no published release.
	EnvRuntimeDir = "STELLA_ML_RUNTIME_DIR" // contains the stella-ml binary + lib/
	EnvModelDir   = "STELLA_ML_MODEL_DIR"   // contains model_int8.onnx + tokenizer.json

	stellaIssueURL = "https://github.com/CherryHQ/stella/issues/new"

	embedModelFile = "model_int8.onnx"
	tokenizerFile  = "tokenizer.json"
	binFile        = "stella-ml"
)

// Resolved is the set of paths a supervisor needs to launch the sidecar.
type Resolved struct {
	BinPath        string // stella-ml executable
	LibDir         string // directory holding libonnxruntime.{dylib,so}
	EmbedModelPath string // e5 embedding model
	TokenizerPath  string // tokenizer.json
	RuntimeVersion string
	ModelVersion   string
}

// Supported reports whether the current platform can run the sidecar at all.
func Supported() bool {
	switch runtime.GOOS {
	case "darwin", "linux":
		return true
	default:
		return false
	}
}

// RuntimeRoot is the install dir for the runtime (binary + onnxruntime) bundle.
func RuntimeRoot(stellaHome string) string {
	return filepath.Join(stellaHome, "ml-runtime", RuntimeVersion+"-"+runtime.GOOS+"-"+runtime.GOARCH)
}

// ModelRoot is the install dir for the model bundle (platform-independent).
func ModelRoot(stellaHome string) string {
	return filepath.Join(stellaHome, "ml-models", ModelVersion)
}

// Resolve returns the sidecar paths, preferring the dev overrides when set and
// otherwise looking under the installed bundles in stellaHome. found is false
// (with no error) when nothing is installed — the caller treats that as "local ML
// unavailable" and disables the feature, exactly like a disabled lane.
func Resolve(stellaHome string) (Resolved, bool, error) {
	if !Supported() {
		return Resolved{}, false, nil
	}

	runtimeDir := os.Getenv(EnvRuntimeDir)
	modelDir := os.Getenv(EnvModelDir)
	if runtimeDir == "" {
		runtimeDir = RuntimeRoot(stellaHome)
	}
	if modelDir == "" {
		modelDir = ModelRoot(stellaHome)
	}

	r := Resolved{
		BinPath:        filepath.Join(runtimeDir, binFile),
		LibDir:         filepath.Join(runtimeDir, "lib"),
		EmbedModelPath: filepath.Join(modelDir, embedModelFile),
		TokenizerPath:  filepath.Join(modelDir, tokenizerFile),
		RuntimeVersion: RuntimeVersion,
		ModelVersion:   ModelVersion,
	}
	// A dev runtime dir may keep the binary at its root with libs alongside; accept
	// either lib/ or the dir itself as the library location.
	if !dirExists(r.LibDir) {
		r.LibDir = runtimeDir
	}

	for _, p := range []string{r.BinPath, r.EmbedModelPath, r.TokenizerPath} {
		if !fileExists(p) {
			return Resolved{}, false, nil
		}
	}
	return r, true, nil
}

// MissingRuntimeHint explains how to obtain the runtime when it is not installed.
func MissingRuntimeHint() string {
	if !Supported() {
		return fmt.Sprintf("The native ML sidecar (local OCR/embedding) is only available on darwin and linux, not %s/%s.", runtime.GOOS, runtime.GOARCH)
	}
	return fmt.Sprintf("No ML runtime is installed. Run `stellad ml download-runtime`, or set %s and %s to a locally built sidecar and models. File an issue if download fails: %s", EnvRuntimeDir, EnvModelDir, stellaIssueURL)
}

func fileExists(p string) bool {
	info, err := os.Stat(p)
	return err == nil && !info.IsDir()
}

func dirExists(p string) bool {
	info, err := os.Stat(p)
	return err == nil && info.IsDir()
}
