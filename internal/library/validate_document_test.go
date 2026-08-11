package library

import (
	"archive/zip"
	"errors"
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
