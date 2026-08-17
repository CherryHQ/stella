package library

import (
	"archive/zip"
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestFormatRegistryMapsEverySupportedExtension(t *testing.T) {
	t.Parallel()
	tests := map[string]string{
		"a.txt": MediaTypeText, "a.md": MediaTypeMarkdown, "a.markdown": MediaTypeMarkdown,
		"a.pdf": MediaTypePDF, "a.doc": MediaTypeDOC, "a.docx": MediaTypeDOCX,
		"a.odt": MediaTypeODT, "a.rtf": MediaTypeRTF, "a.xls": MediaTypeXLS,
		"a.xlsx": MediaTypeXLSX, "a.ods": MediaTypeODS, "a.csv": MediaTypeCSV,
		"a.tsv": MediaTypeTSV, "a.ppt": MediaTypePPT, "a.pptx": MediaTypePPTX, "a.odp": MediaTypeODP,
		"a.html": MediaTypeHTML, "a.htm": MediaTypeHTML,
		"a.xhtml": MediaTypeXHTML, "a.epub": MediaTypeEPUB, "a.fb2": MediaTypeFB2,
		"a.mdx": MediaTypeMDX, "a.rst": MediaTypeRST, "a.org": MediaTypeORG,
		"a.json": MediaTypeJSON, "a.yaml": MediaTypeYAML, "a.yml": MediaTypeYAML,
		"a.toml": MediaTypeTOML, "a.xml": MediaTypeXML,
	}
	for name, want := range tests {
		_, got, err := validateUploadName(strings.ToUpper(name))
		if err != nil || got != want {
			t.Errorf("validateUploadName(%q) = %q, %v; want %q", name, got, err, want)
		}
	}
	if len(SupportedMediaTypes()) != len(formatSpecs) {
		t.Fatalf("supported media types = %d, want %d", len(SupportedMediaTypes()), len(formatSpecs))
	}
	if slices.Contains(XbergMediaTypes(), MediaTypeText) || slices.Contains(XbergMediaTypes(), MediaTypeMarkdown) {
		t.Fatal("built-in text formats leaked into Xberg routes")
	}
}

func TestValidateStructuredTextFormats(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		mediaType string
		valid     string
		invalid   string
	}{
		{"rtf", MediaTypeRTF, `{\rtf1 hello}`, `plain text`},
		{"csv", MediaTypeCSV, "name,value\nalpha,1\n", "name,\"unterminated\n"},
		{"tsv", MediaTypeTSV, "name\tvalue\nalpha\t1\n", "name\t\x00\n"},
		{"html", MediaTypeHTML, "<html><body>Hello</body></html>", "plain text"},
		{"xhtml", MediaTypeXHTML, `<html xmlns="http://www.w3.org/1999/xhtml"><body/></html>`, `<body/>`},
		{"fb2", MediaTypeFB2, `<FictionBook><body/></FictionBook>`, `<book/>`},
		{"json", MediaTypeJSON, `{"enabled":true}`, `{"enabled":}`},
		{"yaml", MediaTypeYAML, "enabled: true\n", "key: [\n"},
		{"toml", MediaTypeTOML, "enabled = true\n", "enabled =\n"},
		{"xml", MediaTypeXML, `<root><item/></root>`, `<root>`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			valid := filepath.Join(t.TempDir(), "valid")
			if err := os.WriteFile(valid, []byte(test.valid), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := validateUploadFile(valid, test.mediaType); err != nil {
				t.Fatalf("valid document rejected: %v", err)
			}
			invalid := filepath.Join(t.TempDir(), "invalid")
			if err := os.WriteFile(invalid, []byte(test.invalid), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := validateUploadFile(invalid, test.mediaType); !errors.Is(err, ErrInvalidFile) {
				t.Fatalf("invalid document error = %v, want ErrInvalidFile", err)
			}
		})
	}
}

func TestValidateOfficeZIPPackagesAndRejectsTraversal(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		mediaType string
		entries   map[string]string
	}{
		{"xlsx", MediaTypeXLSX, map[string]string{"[Content_Types].xml": "<Types/>", "xl/workbook.xml": "<workbook/>"}},
		{"pptx", MediaTypePPTX, map[string]string{"[Content_Types].xml": "<Types/>", "ppt/presentation.xml": "<presentation/>"}},
		{"odt", MediaTypeODT, map[string]string{"mimetype": "application/vnd.oasis.opendocument.text", "content.xml": "<document/>"}},
		{"ods", MediaTypeODS, map[string]string{"mimetype": "application/vnd.oasis.opendocument.spreadsheet", "content.xml": "<document/>"}},
		{"odp", MediaTypeODP, map[string]string{"mimetype": "application/vnd.oasis.opendocument.presentation", "content.xml": "<document/>"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			file := writeZIPFixture(t, test.entries)
			if err := validateUploadFile(file, test.mediaType); err != nil {
				t.Fatal(err)
			}
		})
	}
	traversal := writeZIPFixture(t, map[string]string{
		"[Content_Types].xml": "<Types/>", "word/document.xml": "<document/>", "../escape": "bad",
	})
	if err := validateUploadFile(traversal, MediaTypeDOCX); !errors.Is(err, ErrInvalidFile) {
		t.Fatalf("traversal error = %v, want ErrInvalidFile", err)
	}
	renamedXLSX := writeZIPFixture(t, map[string]string{
		"[Content_Types].xml": "<Types/>", "xl/workbook.xml": "<workbook/>",
	})
	if err := validateUploadFile(renamedXLSX, MediaTypeDOCX); !errors.Is(err, ErrInvalidFile) {
		t.Fatalf("renamed XLSX error = %v, want ErrInvalidFile", err)
	}
}

func TestValidateEPUBRequiresDeclaredRootfile(t *testing.T) {
	t.Parallel()
	container := `<?xml version="1.0"?><container><rootfiles><rootfile full-path="OPS/package.opf"/></rootfiles></container>`
	valid := writeZIPFixture(t, map[string]string{
		"mimetype": "application/epub+zip", "META-INF/container.xml": container, "OPS/package.opf": "<package/>",
	})
	if err := validateUploadFile(valid, MediaTypeEPUB); err != nil {
		t.Fatal(err)
	}
	missing := writeZIPFixture(t, map[string]string{
		"mimetype": "application/epub+zip", "META-INF/container.xml": container,
	})
	if err := validateUploadFile(missing, MediaTypeEPUB); !errors.Is(err, ErrInvalidFile) {
		t.Fatalf("missing rootfile error = %v, want ErrInvalidFile", err)
	}
}

func TestValidateLegacyOfficeRequiresFormatSpecificStream(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		mediaType string
		stream    string
	}{
		{MediaTypeDOC, "WordDocument"},
		{MediaTypeXLS, "Workbook"},
		{MediaTypePPT, "PowerPoint Document"},
	} {
		valid := writeCFBFixture(t, test.stream)
		if err := validateUploadFile(valid, test.mediaType); err != nil {
			t.Fatalf("%s valid fixture: %v", test.mediaType, err)
		}
		wrong := writeCFBFixture(t, "UnrelatedStream")
		if err := validateUploadFile(wrong, test.mediaType); !errors.Is(err, ErrInvalidFile) {
			t.Fatalf("%s wrong stream error = %v, want ErrInvalidFile", test.mediaType, err)
		}
	}
}

func writeZIPFixture(t *testing.T, entries map[string]string) string {
	t.Helper()
	name := filepath.Join(t.TempDir(), "fixture.zip")
	file, err := os.Create(name)
	if err != nil {
		t.Fatal(err)
	}
	writer := zip.NewWriter(file)
	for entryName, content := range entries {
		entry, err := writer.Create(entryName)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := entry.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	return name
}

// writeCFBFixture emits the smallest sector-backed CFB accepted by the reader:
// one FAT sector, one directory sector, and an eight-sector 4096-byte stream.
func writeCFBFixture(t *testing.T, streamName string) string {
	t.Helper()
	const sectorSize = 512
	data := make([]byte, sectorSize*11)
	copy(data[:8], []byte{0xd0, 0xcf, 0x11, 0xe0, 0xa1, 0xb1, 0x1a, 0xe1})
	binary.LittleEndian.PutUint16(data[24:26], 0x003e)
	binary.LittleEndian.PutUint16(data[26:28], 3)
	binary.LittleEndian.PutUint16(data[28:30], 0xfffe)
	binary.LittleEndian.PutUint16(data[30:32], 9)
	binary.LittleEndian.PutUint16(data[32:34], 6)
	binary.LittleEndian.PutUint32(data[44:48], 1)
	binary.LittleEndian.PutUint32(data[48:52], 1)
	binary.LittleEndian.PutUint32(data[56:60], 4096)
	binary.LittleEndian.PutUint32(data[60:64], 0xfffffffe)
	binary.LittleEndian.PutUint32(data[68:72], 0xfffffffe)
	for offset := 76; offset < sectorSize; offset += 4 {
		binary.LittleEndian.PutUint32(data[offset:offset+4], 0xffffffff)
	}
	binary.LittleEndian.PutUint32(data[76:80], 0)

	fat := data[sectorSize : sectorSize*2]
	for offset := 0; offset < len(fat); offset += 4 {
		binary.LittleEndian.PutUint32(fat[offset:offset+4], 0xffffffff)
	}
	binary.LittleEndian.PutUint32(fat[0:4], 0xfffffffd)
	binary.LittleEndian.PutUint32(fat[4:8], 0xfffffffe)
	for sector := uint32(2); sector < 9; sector++ {
		binary.LittleEndian.PutUint32(fat[sector*4:sector*4+4], sector+1)
	}
	binary.LittleEndian.PutUint32(fat[9*4:9*4+4], 0xfffffffe)

	directory := data[sectorSize*2 : sectorSize*3]
	writeCFBDirectoryEntry(directory[0:128], "Root Entry", 5, 1, 0xfffffffe, 0)
	writeCFBDirectoryEntry(directory[128:256], streamName, 2, 0xffffffff, 2, 4096)
	name := filepath.Join(t.TempDir(), "fixture.cfb")
	if err := os.WriteFile(name, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return name
}

func writeCFBDirectoryEntry(entry []byte, name string, objectType byte, childID, startingSector uint32, size uint64) {
	runes := []rune(name + "\x00")
	for index, value := range runes {
		binary.LittleEndian.PutUint16(entry[index*2:index*2+2], uint16(value))
	}
	binary.LittleEndian.PutUint16(entry[64:66], uint16(len(runes)*2))
	entry[66] = objectType
	entry[67] = 1
	binary.LittleEndian.PutUint32(entry[68:72], 0xffffffff)
	binary.LittleEndian.PutUint32(entry[72:76], 0xffffffff)
	binary.LittleEndian.PutUint32(entry[76:80], childID)
	binary.LittleEndian.PutUint32(entry[116:120], startingSector)
	binary.LittleEndian.PutUint64(entry[120:128], size)
}
