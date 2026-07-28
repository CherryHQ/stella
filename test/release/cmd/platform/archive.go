package main

import (
	"archive/tar"
	"bufio"
	"compress/gzip"
	"debug/elf"
	"debug/macho"
	"debug/pe"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	releasecontract "github.com/CherryHQ/stella/test/release"
)

const archiveReportSchemaVersion = 1

type archiveReport struct {
	SchemaVersion int                 `json:"schema_version"`
	Run           releasecontract.Run `json:"run"`
	CheckedAt     time.Time           `json:"checked_at"`
	ChecksumFiles int                 `json:"checksum_files"`
	Archives      []archiveInspection `json:"archives"`
	Error         string              `json:"error,omitempty"`
}

type archiveInspection struct {
	Platform     string   `json:"platform"`
	Name         string   `json:"name"`
	SHA256       string   `json:"sha256"`
	Binary       string   `json:"binary"`
	BinaryFormat string   `json:"binary_format"`
	Machine      string   `json:"machine"`
	Files        []string `json:"files"`
}

func verifyArchiveMatrix(
	root string,
	manifest releasecontract.CandidateManifest,
) (archiveReport, error) {
	report := archiveReport{
		SchemaVersion: archiveReportSchemaVersion,
		Run:           manifest.Run,
		CheckedAt:     time.Now().UTC(),
	}
	checksums, err := candidateChecksums(root, manifest)
	if err != nil {
		return report, err
	}
	report.ChecksumFiles = len(checksums)

	var archives []releasecontract.CandidateFile
	expectedChecksums := map[string]string{}
	for _, file := range manifest.Files {
		switch file.Kind {
		case "archive":
			archives = append(archives, file)
			expectedChecksums[file.Name] = file.SHA256
		case "linux_package":
			expectedChecksums[file.Name] = file.SHA256
		}
	}
	if len(checksums) != len(expectedChecksums) {
		return report, fmt.Errorf(
			"candidate checksum file has %d entries, want %d archives and packages",
			len(checksums),
			len(expectedChecksums),
		)
	}
	for name, want := range expectedChecksums {
		got, ok := checksums[name]
		if !ok {
			return report, fmt.Errorf("candidate checksum file is missing %s", name)
		}
		if got != want {
			return report, fmt.Errorf("candidate checksum for %s does not match its immutable manifest", name)
		}
	}

	sort.Slice(archives, func(i, j int) bool {
		return archives[i].OS+"/"+archives[i].Arch < archives[j].OS+"/"+archives[j].Arch
	})
	for _, candidate := range archives {
		inspection, err := inspectCandidateArchive(root, candidate, "")
		if err != nil {
			return report, err
		}
		report.Archives = append(report.Archives, inspection)
	}
	if len(report.Archives) != 6 {
		return report, fmt.Errorf("inspected %d candidate archives, want 6", len(report.Archives))
	}
	return report, nil
}

func extractCandidateBinary(
	root string,
	manifest releasecontract.CandidateManifest,
	platform releasecontract.Platform,
	output string,
) error {
	var candidate *releasecontract.CandidateFile
	for i := range manifest.Files {
		file := &manifest.Files[i]
		if file.Kind == "archive" && file.OS == platform.OS && file.Arch == platform.Arch {
			candidate = file
			break
		}
	}
	if candidate == nil {
		return fmt.Errorf("candidate archive for %s/%s is missing", platform.OS, platform.Arch)
	}
	relative, err := filepath.Rel(root, output)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return fmt.Errorf("candidate extraction output must stay inside the repository")
	}
	if err := os.MkdirAll(filepath.Dir(output), 0o755); err != nil {
		return fmt.Errorf("create candidate extraction directory: %w", err)
	}
	inspection, err := inspectCandidateArchive(root, *candidate, output)
	if err != nil {
		return err
	}
	fmt.Printf(
		"extracted %s candidate %s (%s/%s)\n",
		inspection.Platform,
		inspection.Binary,
		inspection.BinaryFormat,
		inspection.Machine,
	)
	return nil
}

func inspectCandidateArchive(
	root string,
	candidate releasecontract.CandidateFile,
	output string,
) (_ archiveInspection, returnErr error) {
	archivePath := filepath.Join(root, filepath.FromSlash(candidate.Path))
	file, err := os.Open(archivePath)
	if err != nil {
		return archiveInspection{}, fmt.Errorf("open candidate archive %s: %w", candidate.Name, err)
	}
	defer func() {
		if err := file.Close(); err != nil && returnErr == nil {
			returnErr = err
		}
	}()
	gzipReader, err := gzip.NewReader(file)
	if err != nil {
		return archiveInspection{}, fmt.Errorf("open candidate archive gzip %s: %w", candidate.Name, err)
	}
	defer func() {
		if err := gzipReader.Close(); err != nil && returnErr == nil {
			returnErr = err
		}
	}()

	binaryName := "stellad"
	if candidate.OS == "windows" {
		binaryName += ".exe"
	}
	tempDir, err := os.MkdirTemp("", "stella-candidate-binary-*")
	if err != nil {
		return archiveInspection{}, err
	}
	defer func() { _ = os.RemoveAll(tempDir) }()
	inspectionPath := filepath.Join(tempDir, binaryName)

	seen := map[string]struct{}{}
	var files []string
	binaryFound := false
	reader := tar.NewReader(gzipReader)
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return archiveInspection{}, fmt.Errorf("read candidate archive %s: %w", candidate.Name, err)
		}
		name := path.Clean(header.Name)
		if name == "." || path.IsAbs(name) || name == ".." ||
			strings.HasPrefix(name, "../") || strings.Contains(name, `\`) {
			return archiveInspection{}, fmt.Errorf("candidate archive %s has unsafe path %q", candidate.Name, header.Name)
		}
		if _, exists := seen[name]; exists {
			return archiveInspection{}, fmt.Errorf("candidate archive %s repeats %s", candidate.Name, name)
		}
		seen[name] = struct{}{}
		switch header.Typeflag {
		case tar.TypeDir:
			continue
		case tar.TypeReg, 0:
			// A zero type flag is the legacy tar spelling for a regular file.
			files = append(files, name)
		default:
			return archiveInspection{}, fmt.Errorf(
				"candidate archive %s contains unsupported entry %s of type %d",
				candidate.Name,
				name,
				header.Typeflag,
			)
		}
		if name != binaryName {
			continue
		}
		if header.FileInfo().Mode()&0o111 == 0 {
			return archiveInspection{}, fmt.Errorf("candidate binary %s is not executable in %s", binaryName, candidate.Name)
		}
		binaryFile, err := os.OpenFile(inspectionPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o755)
		if err != nil {
			return archiveInspection{}, err
		}
		_, copyErr := io.Copy(binaryFile, reader)
		closeErr := binaryFile.Close()
		if copyErr != nil || closeErr != nil {
			return archiveInspection{}, errors.Join(copyErr, closeErr)
		}
		binaryFound = true
	}
	for _, required := range []string{"LICENSE", "README.md", "README.zh.md", binaryName} {
		if _, ok := seen[required]; !ok {
			return archiveInspection{}, fmt.Errorf("candidate archive %s is missing %s", candidate.Name, required)
		}
	}
	if !binaryFound {
		return archiveInspection{}, fmt.Errorf("candidate archive %s has no %s payload", candidate.Name, binaryName)
	}

	binaryFormat, machine, err := inspectBinaryMachine(inspectionPath, candidate.OS, candidate.Arch)
	if err != nil {
		return archiveInspection{}, fmt.Errorf("inspect %s binary in %s: %w", candidate.OS, candidate.Name, err)
	}
	if output != "" {
		if err := installExtractedBinary(inspectionPath, output); err != nil {
			return archiveInspection{}, err
		}
	}
	sort.Strings(files)
	return archiveInspection{
		Platform:     candidate.OS + "/" + candidate.Arch,
		Name:         candidate.Name,
		SHA256:       candidate.SHA256,
		Binary:       binaryName,
		BinaryFormat: binaryFormat,
		Machine:      machine,
		Files:        files,
	}, nil
}

func inspectBinaryMachine(filePath, goos, goarch string) (string, string, error) {
	switch goos {
	case "linux":
		file, err := elf.Open(filePath)
		if err != nil {
			return "", "", err
		}
		defer func() { _ = file.Close() }()
		want := map[string]elf.Machine{"amd64": elf.EM_X86_64, "arm64": elf.EM_AARCH64}[goarch]
		if want == 0 || file.Machine != want {
			return "", "", fmt.Errorf("ELF machine is %s, want %s", file.Machine, want)
		}
		return "elf", strings.ToLower(strings.TrimPrefix(file.Machine.String(), "EM_")), nil
	case "darwin":
		file, err := macho.Open(filePath)
		if err != nil {
			return "", "", err
		}
		defer func() { _ = file.Close() }()
		want := map[string]macho.Cpu{"amd64": macho.CpuAmd64, "arm64": macho.CpuArm64}[goarch]
		if want == 0 || file.Cpu != want {
			return "", "", fmt.Errorf("Mach-O CPU is %s, want %s", file.Cpu, want)
		}
		return "mach-o", strings.ToLower(file.Cpu.String()), nil
	case "windows":
		file, err := pe.Open(filePath)
		if err != nil {
			return "", "", err
		}
		defer func() { _ = file.Close() }()
		want := map[string]uint16{
			"amd64": pe.IMAGE_FILE_MACHINE_AMD64,
			"arm64": pe.IMAGE_FILE_MACHINE_ARM64,
		}[goarch]
		if want == 0 || file.Machine != want {
			return "", "", fmt.Errorf("PE machine is %#x, want %#x", file.Machine, want)
		}
		return "pe", fmt.Sprintf("%#x", file.Machine), nil
	default:
		return "", "", fmt.Errorf("unsupported candidate OS %q", goos)
	}
}

func installExtractedBinary(source, target string) error {
	sourceFile, err := os.Open(source)
	if err != nil {
		return err
	}
	defer func() { _ = sourceFile.Close() }()
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return fmt.Errorf("create extracted candidate parent: %w", err)
	}
	targetFile, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o755)
	if err != nil {
		return fmt.Errorf("create extracted candidate %s: %w", target, err)
	}
	_, copyErr := io.Copy(targetFile, sourceFile)
	syncErr := targetFile.Sync()
	closeErr := targetFile.Close()
	if err := errors.Join(copyErr, syncErr, closeErr); err != nil {
		return fmt.Errorf("install extracted candidate %s: %w", target, err)
	}
	return nil
}

func candidateChecksums(
	root string,
	manifest releasecontract.CandidateManifest,
) (map[string]string, error) {
	var checksumFile *releasecontract.CandidateFile
	for i := range manifest.Files {
		if manifest.Files[i].Kind == "checksum" {
			checksumFile = &manifest.Files[i]
			break
		}
	}
	if checksumFile == nil {
		return nil, fmt.Errorf("candidate checksum file is missing")
	}
	file, err := os.Open(filepath.Join(root, filepath.FromSlash(checksumFile.Path)))
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()

	checksums := map[string]string{}
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) != 2 || len(fields[0]) != 64 {
			return nil, fmt.Errorf("invalid candidate checksum line %q", scanner.Text())
		}
		name := strings.TrimPrefix(fields[1], "*")
		if filepath.Base(name) != name {
			return nil, fmt.Errorf("candidate checksum name %q must be a basename", name)
		}
		if _, exists := checksums[name]; exists {
			return nil, fmt.Errorf("candidate checksum repeats %s", name)
		}
		checksums[name] = strings.ToLower(fields[0])
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return checksums, nil
}

func writeArchiveResult(
	root string,
	run releasecontract.Run,
	attempt int,
	startedAt time.Time,
	report archiveReport,
	verifyErr error,
) error {
	report.SchemaVersion = archiveReportSchemaVersion
	report.Run = run
	report.CheckedAt = time.Now().UTC()
	if verifyErr != nil {
		report.Error = verifyErr.Error()
	}
	reportPath := filepath.Join(
		releasecontract.RunDirectory(root, run.ID),
		"artifacts",
		"platform",
		fmt.Sprintf("archive-matrix-a%03d.json", attempt),
	)
	if err := writeExclusiveJSON(reportPath, report); err != nil {
		return errors.Join(verifyErr, err)
	}
	artifact, err := artifactForPath(root, run.ID, "archive-report", reportPath)
	if err != nil {
		return errors.Join(verifyErr, err)
	}

	status := releasecontract.StatusPass
	reason := ""
	if verifyErr != nil {
		status = releasecontract.StatusProductFailure
		reason = oneLine(verifyErr.Error())
	} else if attempt > 1 {
		status = releasecontract.StatusFlaky
		reason = fmt.Sprintf("release workflow attempt %d passed after a retry", attempt)
	}
	result := releasecontract.Result{
		SchemaVersion: releasecontract.SchemaVersion,
		Run:           run,
		Platforms: []releasecontract.Platform{
			{OS: "darwin", Arch: "amd64"},
			{OS: "darwin", Arch: "arm64"},
			{OS: "linux", Arch: "amd64"},
			{OS: "linux", Arch: "arm64"},
			{OS: "windows", Arch: "amd64"},
			{OS: "windows", Arch: "arm64"},
		},
		CapabilityID: "X18",
		ScenarioID:   "X18-S01",
		Runner: releasecontract.Runner{
			Kind: releasecontract.RunnerPackage,
			Name: "release-archive-matrix",
		},
		Attempt:    attempt,
		StartedAt:  startedAt.UTC(),
		FinishedAt: time.Now().UTC(),
		Status:     status,
		Reason:     reason,
		Artifacts:  []releasecontract.Artifact{artifact},
	}
	if _, err := releasecontract.WriteResult(releasecontract.RunDirectory(root, run.ID), result); err != nil {
		return errors.Join(verifyErr, err)
	}
	if err := releasecontract.ValidateArtifactFiles(root, result); err != nil {
		return errors.Join(verifyErr, err)
	}
	if verifyErr != nil {
		return fmt.Errorf("candidate archive matrix failed; see %s: %w", reportPath, verifyErr)
	}
	if status != releasecontract.StatusPass {
		return fmt.Errorf("candidate archive matrix is %s and requires release-owner review", status)
	}
	fmt.Printf("candidate archive matrix passed: %d archives, %d checksums\n", len(report.Archives), report.ChecksumFiles)
	return nil
}
