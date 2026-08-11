package library

import (
	"archive/zip"
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestValidatePDFAndDOCXSignatures(t *testing.T) {
	pdf := filepath.Join(t.TempDir(), "document.pdf")
	if err := os.WriteFile(pdf, []byte("%PDF-1.7\nbody"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := validateUploadFile(pdf, MediaTypePDF); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(pdf, []byte("not pdf"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := validateUploadFile(pdf, MediaTypePDF); !errors.Is(err, ErrInvalidFile) {
		t.Fatalf("invalid PDF error = %v", err)
	}

	docx := filepath.Join(t.TempDir(), "document.docx")
	file, err := os.Create(docx)
	if err != nil {
		t.Fatal(err)
	}
	writer := zip.NewWriter(file)
	for _, name := range []string{"[Content_Types].xml", "word/document.xml"} {
		entry, createErr := writer.Create(name)
		if createErr != nil {
			t.Fatal(createErr)
		}
		if _, writeErr := entry.Write([]byte("<xml/>")); writeErr != nil {
			t.Fatal(writeErr)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if err := validateUploadFile(docx, MediaTypeDOCX); err != nil {
		t.Fatal(err)
	}
}

func TestValidateUploadNameAcceptsDocumentExtensions(t *testing.T) {
	for name, want := range map[string]string{"paper.PDF": MediaTypePDF, "report.docx": MediaTypeDOCX} {
		_, got, err := validateUploadName(name)
		if err != nil || got != want {
			t.Fatalf("validateUploadName(%q) = %q, %v", name, got, err)
		}
	}
}

func TestValidateDOCXBoundsExpandedContent(t *testing.T) {
	tests := []struct {
		name    string
		method  uint16
		entries map[string]int64
	}{
		{
			name: "oversized entry", method: zip.Store,
			entries: map[string]int64{
				"[Content_Types].xml": 1,
				"word/document.xml":   maxDOCXEntryUncompressedBytes + 1,
			},
		},
		{
			name: "oversized aggregate", method: zip.Store,
			entries: map[string]int64{
				"[Content_Types].xml": 24 << 20,
				"word/document.xml":   24 << 20,
				"word/styles.xml":     24 << 20,
			},
		},
		{
			name: "excessive compression ratio", method: zip.Deflate,
			entries: map[string]int64{
				"[Content_Types].xml": 1,
				"word/document.xml":   1 << 20,
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "bounded.docx")
			file, err := os.Create(path)
			if err != nil {
				t.Fatal(err)
			}
			writer := zip.NewWriter(file)
			for name, size := range test.entries {
				entry, createErr := writer.CreateHeader(&zip.FileHeader{Name: name, Method: test.method})
				if createErr != nil {
					t.Fatal(createErr)
				}
				if _, copyErr := io.CopyN(entry, repeatedByteReader{'x'}, size); copyErr != nil {
					t.Fatal(copyErr)
				}
			}
			if err := writer.Close(); err != nil {
				t.Fatal(err)
			}
			if err := file.Close(); err != nil {
				t.Fatal(err)
			}
			if err := validateDOCXFile(path); !errors.Is(err, ErrInvalidFile) {
				t.Fatalf("validateDOCXFile error = %v, want ErrInvalidFile", err)
			}
		})
	}
}

func TestValidateDOCXReadsEntriesInsteadOfTrustingDirectorySizes(t *testing.T) {
	var archive bytes.Buffer
	writer := zip.NewWriter(&archive)
	for _, name := range []string{"[Content_Types].xml", "word/document.xml"} {
		entry, err := writer.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := entry.Write([]byte("document content")); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	data := archive.Bytes()
	centralDirectory := bytes.LastIndex(data, []byte{'P', 'K', 1, 2})
	if centralDirectory < 0 {
		t.Fatal("DOCX fixture has no central directory entry")
	}
	binary.LittleEndian.PutUint32(data[centralDirectory+24:], 1)
	path := filepath.Join(t.TempDir(), "false-size.docx")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := validateDOCXFile(path); !errors.Is(err, ErrInvalidFile) {
		t.Fatalf("validateDOCXFile error = %v, want ErrInvalidFile", err)
	}
}

func TestValidateDOCXAllowsSmallHighRatioEntry(t *testing.T) {
	path := filepath.Join(t.TempDir(), "small.docx")
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	writer := zip.NewWriter(file)
	for name, size := range map[string]int64{
		"[Content_Types].xml": 1,
		"word/document.xml":   minDOCXRatioCheckBytes,
	} {
		entry, err := writer.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := io.CopyN(entry, repeatedByteReader{'x'}, size); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if err := validateDOCXFile(path); err != nil {
		t.Fatalf("validateDOCXFile: %v", err)
	}
}

type repeatedByteReader struct{ value byte }

func (r repeatedByteReader) Read(buffer []byte) (int, error) {
	for i := range buffer {
		buffer[i] = r.value
	}
	return len(buffer), nil
}
