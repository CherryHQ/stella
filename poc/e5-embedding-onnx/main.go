package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

func main() {
	base, _ := os.Getwd()
	if err := initORT(base); err != nil {
		fail(err)
	}
	defer shutdownORT()

	modelFile := "model.onnx"
	if m := os.Getenv("E5_MODEL"); m != "" {
		modelFile = m // e.g. model_int8.onnx, model_fp16.onnx
	}
	eng, err := newE5Engine(filepath.Join(base, "model", modelFile), filepath.Join(base, "model", "tokenizer.json"))
	if err != nil {
		fail(err)
	}
	defer eng.close()

	cmd := "demo"
	if len(os.Args) > 1 {
		cmd = os.Args[1]
	}

	switch cmd {
	case "vec":
		// vec <prefix> <text...>  -> prints the raw embedding (for reference comparison)
		if len(os.Args) < 4 {
			fail(fmt.Errorf("usage: e5 vec <query|passage|-> <text>"))
		}
		prefix := os.Args[2]
		if prefix == "-" {
			prefix = ""
		}
		v, err := eng.Embed(prefix, strings.Join(os.Args[3:], " "))
		if err != nil {
			fail(err)
		}
		parts := make([]string, len(v))
		for i, x := range v {
			parts[i] = strconv.FormatFloat(float64(x), 'f', 6, 32)
		}
		fmt.Println(strings.Join(parts, " "))

	case "demo":
		runDemo(eng)

	default:
		fail(fmt.Errorf("unknown command %q (use: demo | vec)", cmd))
	}
}

// runDemo embeds a small cross-lingual set and prints a cosine similarity
// matrix, demonstrating that semantically equivalent sentences across languages
// land close together.
func runDemo(eng *e5Engine) {
	sents := []string{
		"How do I reset my password?",
		"怎么重置我的密码？",
		"パスワードをリセットするには？",
		"What is the capital of France?",
		"今天天气真好，适合出去散步。",
	}
	vecs := make([][]float32, len(sents))
	for i, s := range sents {
		v, err := eng.Embed("query", s)
		if err != nil {
			fail(err)
		}
		vecs[i] = v
	}
	fmt.Printf("embedded %d sentences, dim=%d\n\n", len(sents), len(vecs[0]))
	fmt.Print("cosine similarity matrix:\n     ")
	for j := range sents {
		fmt.Printf("  [%d]  ", j)
	}
	fmt.Println()
	for i := range sents {
		fmt.Printf("[%d] ", i)
		for j := range sents {
			fmt.Printf("%6.3f ", cosine(vecs[i], vecs[j]))
		}
		fmt.Printf("  %s\n", sents[i])
	}
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "error:", err)
	os.Exit(1)
}
