// Minimal CGO spike: import BOTH native deps and reference symbols so the
// linker must resolve them. Proves zig cc can cross-compile the sidecar.
package main

import (
	"fmt"

	"github.com/daulet/tokenizers"
	ort "github.com/yalue/onnxruntime_go"
)

func main() {
	// force onnxruntime_go cgo glue to link (dlopen path, no lib needed at build)
	ort.SetSharedLibraryPath("unused")
	// force daulet/tokenizers (static libtokenizers.a) to link
	tk, err := tokenizers.FromBytes([]byte(`{"version":"1.0","model":{"type":"WordLevel","vocab":{"a":0},"unk_token":"a"}}`))
	if err != nil {
		fmt.Println("tokenizer err:", err)
		return
	}
	ids, _ := tk.Encode("a", false)
	fmt.Println("ok", len(ids), ort.IsInitialized())
}
