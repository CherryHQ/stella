package main

import (
	"fmt"
	"runtime"

	ort "github.com/yalue/onnxruntime_go"
)

func initORT(baseDir string) error {
	var lib string
	switch runtime.GOOS + "/" + runtime.GOARCH {
	case "darwin/arm64":
		lib = baseDir + "/runtime/onnxruntime-osx-arm64-1.27.0/lib/libonnxruntime.dylib"
	default:
		return fmt.Errorf("unsupported platform %s/%s (POC only ships darwin/arm64)", runtime.GOOS, runtime.GOARCH)
	}
	ort.SetSharedLibraryPath(lib)
	if err := ort.InitializeEnvironment(); err != nil {
		return fmt.Errorf("initialize onnxruntime: %w", err)
	}
	return nil
}

func shutdownORT() { _ = ort.DestroyEnvironment() }
