package main

import (
	"bufio"
	"fmt"
	"os"
)

// loadCharset reads the PP-OCR character dictionary (one entry per line) and
// builds the CTC index->string table.
//
// CTCLabelDecode layout for PP-OCRv5: index 0 is the CTC blank, indices
// 1..N map to dict lines 0..N-1, and the final index is the space character.
// The rec model emits len(dict)+2 classes (18383+2 = 18385).
func loadCharset(path string) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var dict []string
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1024*1024), 1024*1024)
	for sc.Scan() {
		dict = append(dict, sc.Text())
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	if len(dict) == 0 {
		return nil, fmt.Errorf("empty charset %s", path)
	}

	table := make([]string, 0, len(dict)+2)
	table = append(table, "")  // 0: blank
	table = append(table, dict...) // 1..N
	table = append(table, " ")  // N+1: space
	return table, nil
}
