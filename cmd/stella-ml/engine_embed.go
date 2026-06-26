package main

import (
	"fmt"
	"math"

	"github.com/daulet/tokenizers"
	ort "github.com/yalue/onnxruntime_go"
)

// embedDim is multilingual-e5-small's hidden size.
const embedDim = 384

// embedModelID is the canonical vector-space key. It MUST match the space written
// by the main module's local embedding provider, since vectors are namespaced by
// this string in storage.
const embedModelID = "intfloat/multilingual-e5-small@384"

// maxTokens caps sequence length; e5 / XLM-R is trained at 512.
const maxTokens = 512

// e5Engine produces normalized sentence embeddings from the multilingual-e5-small
// ONNX model: tokenize (XLM-RoBERTa) -> encoder -> mean pool -> L2 normalize.
//
// onnxruntime sessions are safe for concurrent Run calls as long as the input and
// output tensors are not shared, so Embed/EmbedBatch take no lock; concurrency is
// bounded by the server's per-endpoint semaphore.
type e5Engine struct {
	tk   *tokenizers.Tokenizer
	sess *ort.DynamicAdvancedSession
	opts *ort.SessionOptions
}

func newE5Engine(modelPath, tokenizerPath string, intraOp, interOp int) (*e5Engine, error) {
	tk, err := tokenizers.FromFile(tokenizerPath)
	if err != nil {
		return nil, fmt.Errorf("load tokenizer: %w", err)
	}
	opts, err := newSessionOptions(intraOp, interOp)
	if err != nil {
		tk.Close()
		return nil, err
	}
	sess, err := ort.NewDynamicAdvancedSession(modelPath,
		[]string{"input_ids", "attention_mask", "token_type_ids"},
		[]string{"last_hidden_state"}, opts)
	if err != nil {
		opts.Destroy()
		tk.Close()
		return nil, fmt.Errorf("e5 session: %w", err)
	}
	return &e5Engine{tk: tk, sess: sess, opts: opts}, nil
}

func (e *e5Engine) close() {
	if e.sess != nil {
		_ = e.sess.Destroy()
	}
	if e.opts != nil {
		e.opts.Destroy()
	}
	if e.tk != nil {
		e.tk.Close()
	}
}

// modePrefix maps a request mode to e5's instruction prefix. e5 is an asymmetric
// retriever: search text is "query", indexed text is "passage".
func modePrefix(mode string) string {
	switch mode {
	case "passage":
		return "passage"
	default:
		return "query"
	}
}

// EmbedBatch encodes texts as a padded batch and returns one L2-normalized vector
// per input, in order. prefix is e5's instruction prefix ("query"/"passage").
func (e *e5Engine) EmbedBatch(prefix string, texts []string) ([][]float32, error) {
	if len(texts) == 0 {
		return nil, nil
	}

	// Tokenize each text, tracking the batch's max length to pad to.
	idsRows := make([][]int64, len(texts))
	maskRows := make([][]int64, len(texts))
	maxLen := 0
	for i, t := range texts {
		if prefix != "" {
			t = prefix + ": " + t
		}
		enc := e.tk.EncodeWithOptions(t, true, tokenizers.WithReturnAttentionMask())
		n := len(enc.IDs)
		if n == 0 {
			return nil, fmt.Errorf("empty tokenization for input %d", i)
		}
		if n > maxTokens {
			n = maxTokens
		}
		ids := make([]int64, n)
		mask := make([]int64, n)
		for j := 0; j < n; j++ {
			ids[j] = int64(enc.IDs[j])
			mask[j] = int64(enc.AttentionMask[j])
		}
		idsRows[i] = ids
		maskRows[i] = mask
		if n > maxLen {
			maxLen = n
		}
	}

	batch := len(texts)
	flatIDs := make([]int64, batch*maxLen)
	flatMask := make([]int64, batch*maxLen)
	flatTType := make([]int64, batch*maxLen) // XLM-R: all zeros
	for i := 0; i < batch; i++ {
		copy(flatIDs[i*maxLen:], idsRows[i])
		copy(flatMask[i*maxLen:], maskRows[i])
		// padding positions stay 0 in ids and mask (mask=0 excludes them from pooling)
	}

	shape := ort.NewShape(int64(batch), int64(maxLen))
	idsT, err := ort.NewTensor(shape, flatIDs)
	if err != nil {
		return nil, err
	}
	defer idsT.Destroy()
	maskT, err := ort.NewTensor(shape, flatMask)
	if err != nil {
		return nil, err
	}
	defer maskT.Destroy()
	ttT, err := ort.NewTensor(shape, flatTType)
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

	// out: [batch, maxLen, embedDim]. Mean pool over masked tokens, then L2.
	hidden := out.GetData()
	vecs := make([][]float32, batch)
	for i := 0; i < batch; i++ {
		vec := make([]float32, embedDim)
		var count float32
		for t := 0; t < maxLen; t++ {
			if flatMask[i*maxLen+t] == 0 {
				continue
			}
			count++
			base := (i*maxLen + t) * embedDim
			for d := 0; d < embedDim; d++ {
				vec[d] += hidden[base+d]
			}
		}
		if count == 0 {
			return nil, fmt.Errorf("zero attention mask for input %d", i)
		}
		for d := range vec {
			vec[d] /= count
		}
		l2normalize(vec)
		vecs[i] = vec
	}
	return vecs, nil
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
