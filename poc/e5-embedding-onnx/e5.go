package main

import (
	"fmt"
	"math"

	"github.com/daulet/tokenizers"
	ort "github.com/yalue/onnxruntime_go"
)

// embedDim is multilingual-e5-small's hidden size.
const embedDim = 384

// e5Engine produces normalized sentence embeddings from the multilingual-e5-small
// ONNX model: tokenize (XLM-RoBERTa) -> encoder -> mean pool -> L2 normalize.
type e5Engine struct {
	tk   *tokenizers.Tokenizer
	sess *ort.DynamicAdvancedSession
}

func newE5Engine(modelPath, tokenizerPath string) (*e5Engine, error) {
	tk, err := tokenizers.FromFile(tokenizerPath)
	if err != nil {
		return nil, fmt.Errorf("load tokenizer: %w", err)
	}
	sess, err := ort.NewDynamicAdvancedSession(modelPath,
		[]string{"input_ids", "attention_mask", "token_type_ids"},
		[]string{"last_hidden_state"}, nil)
	if err != nil {
		return nil, fmt.Errorf("e5 session: %w", err)
	}
	return &e5Engine{tk: tk, sess: sess}, nil
}

func (e *e5Engine) close() {
	if e.sess != nil {
		_ = e.sess.Destroy()
	}
	if e.tk != nil {
		e.tk.Close()
	}
}

// Embed encodes one text. e5 expects an instruction prefix: pass "query" or
// "passage" for asym retrieval; the prefix is prepended as "<prefix>: ".
func (e *e5Engine) Embed(prefix, text string) ([]float32, error) {
	if prefix != "" {
		text = prefix + ": " + text
	}
	enc := e.tk.EncodeWithOptions(text, true, tokenizers.WithReturnAttentionMask())
	n := len(enc.IDs)
	if n == 0 {
		return nil, fmt.Errorf("empty tokenization")
	}

	ids := make([]int64, n)
	mask := make([]int64, n)
	ttid := make([]int64, n) // XLM-R: all zeros
	for i := range enc.IDs {
		ids[i] = int64(enc.IDs[i])
		mask[i] = int64(enc.AttentionMask[i])
	}

	shape := ort.NewShape(1, int64(n))
	idsT, err := ort.NewTensor(shape, ids)
	if err != nil {
		return nil, err
	}
	defer idsT.Destroy()
	maskT, err := ort.NewTensor(shape, mask)
	if err != nil {
		return nil, err
	}
	defer maskT.Destroy()
	ttT, err := ort.NewTensor(shape, ttid)
	if err != nil {
		return nil, err
	}
	defer ttT.Destroy()

	outputs := []ort.Value{nil}
	if err := e.sess.Run([]ort.Value{idsT, maskT, ttT}, outputs); err != nil {
		return nil, fmt.Errorf("run: %w", err)
	}
	out, ok := outputs[0].(*ort.Tensor[float32])
	if !ok {
		return nil, fmt.Errorf("unexpected output type %T", outputs[0])
	}
	defer out.Destroy()

	// out: [1, n, embedDim]. Mean pool over tokens weighted by attention mask.
	hidden := out.GetData()
	vec := make([]float32, embedDim)
	var count float32
	for t := 0; t < n; t++ {
		if mask[t] == 0 {
			continue
		}
		count++
		base := t * embedDim
		for d := 0; d < embedDim; d++ {
			vec[d] += hidden[base+d]
		}
	}
	if count == 0 {
		return nil, fmt.Errorf("zero attention mask")
	}
	for d := range vec {
		vec[d] /= count
	}
	l2normalize(vec)
	return vec, nil
}

func l2normalize(v []float32) {
	var sum float64
	for _, x := range v {
		sum += float64(x) * float64(x)
	}
	norm := float32(math.Sqrt(sum))
	if norm == 0 {
		return
	}
	for i := range v {
		v[i] /= norm
	}
}

func cosine(a, b []float32) float32 {
	var dot float32
	for i := range a {
		dot += a[i] * b[i]
	}
	return dot // inputs are L2-normalized
}
