package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	ucli "github.com/urfave/cli/v2"

	"github.com/CherryHQ/stella/internal/sharedposix"
	"github.com/CherryHQ/stella/internal/storagequal"
)

func storageQualifyCommand() *ucli.Command {
	return &ucli.Command{
		Name: "qualify", Usage: "Run shared-POSIX conformance and benchmark qualification",
		Description: "Runs against two independently mounted client roots. The JSON config declares backend/topology evidence, numeric limits fixed before execution, and failure-injection evidence. A local alias can be a POSIX control but cannot qualify as shared storage.",
		Flags: []ucli.Flag{
			&ucli.PathFlag{Name: "config", Required: true, Usage: "qualification input JSON"},
			&ucli.PathFlag{Name: "output", Usage: "write the qualification record to this new file (default: stdout)"},
		},
		Action: runStorageQualify,
	}
}

func runStorageQualify(c *ucli.Context) error {
	data, err := os.ReadFile(c.Path("config"))
	if err != nil {
		return fmt.Errorf("storage qualify: read config: %w", err)
	}
	var cfg storagequal.Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return fmt.Errorf("storage qualify: decode config: %w", err)
	}
	record, runErr := storagequal.Run(c.Context, cfg)
	out, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return fmt.Errorf("storage qualify: encode record: %w", err)
	}
	out = append(out, '\n')
	if path := c.Path("output"); path != "" {
		if err := writeNewFile(path, out, 0o600); err != nil {
			return fmt.Errorf("storage qualify: write record: %w", err)
		}
	} else if _, err := c.App.Writer.Write(out); err != nil {
		return fmt.Errorf("storage qualify: write output: %w", err)
	}
	if runErr != nil {
		return fmt.Errorf("storage qualify: %w", runErr)
	}
	if !record.OverallPass || !record.QualifiedShared {
		return errors.New("storage qualify: shared POSIX qualification failed; review the record")
	}
	return nil
}

func storageInstallQualificationCommand() *ucli.Command {
	return &ucli.Command{
		Name: "install-qualification", Usage: "Install reviewed shared-POSIX identity and qualification evidence",
		Description: "Run only after reviewing a passing qualification record. Writes fixed evidence files beneath the selected shared STELLA_HOME; it does not provision storage or enable multiple replicas.",
		Flags: []ucli.Flag{
			&ucli.PathFlag{Name: "record", Required: true, Usage: "reviewed passing qualification JSON"},
			&ucli.PathFlag{Name: "root", Required: true, Usage: "shared STELLA_HOME mount"},
		},
		Action: runStorageInstallQualification,
	}
}

func runStorageInstallQualification(c *ucli.Context) error {
	data, err := os.ReadFile(c.Path("record"))
	if err != nil {
		return fmt.Errorf("storage install-qualification: read record: %w", err)
	}
	record, err := storagequal.ParseAndValidateRecord(data)
	if err != nil {
		return fmt.Errorf("storage install-qualification: record is not authoritative: %w", err)
	}
	root, err := filepath.Abs(c.Path("root"))
	if err != nil {
		return fmt.Errorf("storage install-qualification: resolve root: %w", err)
	}
	info, err := os.Lstat(root)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("storage install-qualification: root must be a real directory")
	}
	dir := filepath.Join(root, ".stella-shared-posix")
	if err := os.Mkdir(dir, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
		return fmt.Errorf("storage install-qualification: create state directory: %w", err)
	}
	if info, err := os.Lstat(dir); err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("storage install-qualification: state path must be a real directory")
	}
	identity, _ := json.Marshal(struct {
		NamespaceIdentity string `json:"namespace_identity"`
	}{record.NamespaceIdentity})
	identity = append(identity, '\n')
	if err := replaceFile(filepath.Join(dir, "identity.json"), identity); err != nil {
		return fmt.Errorf("storage install-qualification: install identity: %w", err)
	}
	if err := replaceFile(filepath.Join(dir, "qualification.json"), data); err != nil {
		return fmt.Errorf("storage install-qualification: install qualification: %w", err)
	}
	d, err := os.Open(dir)
	if err != nil {
		return fmt.Errorf("storage install-qualification: open state directory: %w", err)
	}
	err = d.Sync()
	return errors.Join(err, d.Close())
}

func storageWitnessCommand() *ucli.Command {
	return &ucli.Command{
		Name: "witness", Usage: "Publish shared-POSIX freshness from an independent client",
		Description: "Runs continuously on a client/node independent from stellad and atomically advances the qualified namespace witness. A co-located witness does not prove cross-client freshness.",
		Flags: []ucli.Flag{
			&ucli.PathFlag{Name: "root", Required: true, Usage: "independently mounted shared STELLA_HOME"},
			&ucli.StringFlag{Name: "client-id", Required: true, Usage: "stable non-secret witness client identity"},
			&ucli.DurationFlag{Name: "interval", Value: 2 * time.Second, Usage: "publication interval"},
		},
		Action: func(c *ucli.Context) error {
			err := sharedposix.RunWitness(c.Context, c.Path("root"), c.String("client-id"), c.Duration("interval"))
			if errors.Is(err, context.Canceled) {
				return nil
			}
			return fmt.Errorf("storage witness: %w", err)
		},
	}
}

func writeNewFile(path string, data []byte, mode os.FileMode) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	if _, err = f.Write(data); err == nil {
		err = f.Sync()
	}
	return errors.Join(err, f.Close())
}

func replaceFile(path string, data []byte) error {
	dir := filepath.Dir(path)
	f, err := os.CreateTemp(dir, ".install-")
	if err != nil {
		return err
	}
	name := f.Name()
	defer func() { _ = os.Remove(name) }()
	if err = f.Chmod(0o600); err == nil {
		_, err = f.Write(data)
	}
	if err == nil {
		err = f.Sync()
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(name, path)
}
