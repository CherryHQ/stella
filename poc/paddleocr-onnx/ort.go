package main

import (
	"fmt"
	"runtime"

	ort "github.com/yalue/onnxruntime_go"
)

// initORT points the wrapper at the bundled shared library and starts the
// global ONNX Runtime environment. Call shutdownORT once at exit.
func initORT(baseDir string) error {
	lib, err := resolveORTLib(baseDir)
	if err != nil {
		return err
	}
	ort.SetSharedLibraryPath(lib)
	if err := ort.InitializeEnvironment(); err != nil {
		return fmt.Errorf("initialize onnxruntime: %w", err)
	}
	return nil
}

func shutdownORT() { _ = ort.DestroyEnvironment() }

func resolveORTLib(baseDir string) (string, error) {
	switch runtime.GOOS + "/" + runtime.GOARCH {
	case "darwin/arm64":
		return baseDir + "/runtime/onnxruntime-osx-arm64-1.27.0/lib/libonnxruntime.dylib", nil
	default:
		return "", fmt.Errorf("unsupported platform %s/%s (POC only ships darwin/arm64)", runtime.GOOS, runtime.GOARCH)
	}
}

// runModel feeds a single NCHW float32 input tensor through a dynamic session
// and returns the first output's data and shape. The model is expected to have
// one input and one output, which is true for PP-OCR det and rec.
func runModel(sess *ort.DynamicAdvancedSession, shape ort.Shape, data []float32) ([]float32, []int64, error) {
	in, err := ort.NewTensor(shape, data)
	if err != nil {
		return nil, nil, fmt.Errorf("new input tensor: %w", err)
	}
	defer in.Destroy()

	outputs := []ort.Value{nil}
	if err := sess.Run([]ort.Value{in}, outputs); err != nil {
		return nil, nil, fmt.Errorf("run: %w", err)
	}
	out, ok := outputs[0].(*ort.Tensor[float32])
	if !ok {
		return nil, nil, fmt.Errorf("unexpected output type %T", outputs[0])
	}
	defer out.Destroy()

	return out.GetData(), out.GetShape(), nil
}
