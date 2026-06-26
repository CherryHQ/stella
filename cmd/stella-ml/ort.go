package main

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	ort "github.com/yalue/onnxruntime_go"
)

// libFileName is the platform-specific onnxruntime shared object. onnxruntime_go
// dlopens it at runtime (no link-time dependency), so the only thing we must do is
// point it at the file shipped in the runtime bundle.
func libFileName() (string, error) {
	switch runtime.GOOS {
	case "darwin":
		return "libonnxruntime.dylib", nil
	case "linux":
		return "libonnxruntime.so", nil
	default:
		return "", fmt.Errorf("stella-ml: unsupported platform %s/%s (only darwin, linux)", runtime.GOOS, runtime.GOARCH)
	}
}

// initORT locates libonnxruntime under libDir (or an explicit file) and
// initializes the global environment. Call shutdownORT once on exit.
func initORT(libPath string) error {
	if libPath == "" {
		return fmt.Errorf("stella-ml: onnxruntime library path is empty")
	}
	if info, err := os.Stat(libPath); err == nil && info.IsDir() {
		name, err := libFileName()
		if err != nil {
			return err
		}
		libPath = filepath.Join(libPath, name)
	}
	if _, err := os.Stat(libPath); err != nil {
		return fmt.Errorf("stella-ml: onnxruntime library not found at %s: %w", libPath, err)
	}
	ort.SetSharedLibraryPath(libPath)
	if err := ort.InitializeEnvironment(); err != nil {
		return fmt.Errorf("stella-ml: initialize onnxruntime: %w", err)
	}
	return nil
}

func shutdownORT() { _ = ort.DestroyEnvironment() }

// newSessionOptions pins the intra/inter-op thread counts instead of taking ORT's
// defaults (which spawn a thread per core and oversubscribe a shared sidecar). A
// value <= 0 leaves ORT's default for that knob.
func newSessionOptions(intraOp, interOp int) (*ort.SessionOptions, error) {
	opts, err := ort.NewSessionOptions()
	if err != nil {
		return nil, fmt.Errorf("stella-ml: session options: %w", err)
	}
	if intraOp > 0 {
		if err := opts.SetIntraOpNumThreads(intraOp); err != nil {
			opts.Destroy()
			return nil, fmt.Errorf("stella-ml: set intra-op threads: %w", err)
		}
	}
	if interOp > 0 {
		if err := opts.SetInterOpNumThreads(interOp); err != nil {
			opts.Destroy()
			return nil, fmt.Errorf("stella-ml: set inter-op threads: %w", err)
		}
	}
	return opts, nil
}
