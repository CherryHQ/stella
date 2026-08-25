package main

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"time"
)

// phaseJournal is deliberately a tiny crash breadcrumb, not a trace. Its
// fixed vocabulary prevents trial identifiers, request data, errors, and
// secrets from becoming a durable diagnostic artifact.
type phaseJournal struct {
	file *os.File
}

type driverPhase string

const (
	phaseDriverStart       driverPhase = "driver_start"
	phaseStreamStart       driverPhase = "stream_start"
	phaseStreamReturn      driverPhase = "stream_return"
	phaseTimeoutStart      driverPhase = "timeout_start"
	phaseStopStart         driverPhase = "stop_start"
	phaseStopReturn        driverPhase = "stop_return"
	phaseSurfaceStart      driverPhase = "surface_start"
	phaseSurfaceReturn     driverPhase = "surface_return"
	phaseAdmissionStart    driverPhase = "admission_start"
	phaseAdmissionReturn   driverPhase = "admission_return"
	phaseEvidenceStart     driverPhase = "evidence_start"
	phaseEvidenceReturn    driverPhase = "evidence_return"
	phaseCleanupStart      driverPhase = "cleanup_start"
	phaseCleanupReturn     driverPhase = "cleanup_return"
	phaseResultDeferStart  driverPhase = "result_defer_start"
	phaseResultWriteStart  driverPhase = "result_write_start"
	phaseResultWriteReturn driverPhase = "result_write_return"
	phaseDriverExit        driverPhase = "driver_exit"
)

var driverPhases = map[driverPhase]struct{}{
	phaseDriverStart: {}, phaseStreamStart: {}, phaseStreamReturn: {}, phaseTimeoutStart: {},
	phaseStopStart: {}, phaseStopReturn: {}, phaseSurfaceStart: {}, phaseSurfaceReturn: {},
	phaseAdmissionStart: {}, phaseAdmissionReturn: {}, phaseEvidenceStart: {}, phaseEvidenceReturn: {},
	phaseCleanupStart: {}, phaseCleanupReturn: {}, phaseResultDeferStart: {}, phaseResultWriteStart: {},
	phaseResultWriteReturn: {}, phaseDriverExit: {},
}

type phaseJournalEntry struct {
	Version   int         `json:"version"`
	Phase     driverPhase `json:"phase"`
	Timestamp string      `json:"timestamp"`
}

func openPhaseJournal(path string) (*phaseJournal, error) {
	if path == "" || filepath.Dir(path) == "." {
		return nil, errors.New("invalid phase journal path")
	}
	if info, err := os.Lstat(path); err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
			return nil, errors.New("unsafe phase journal")
		}
		// A trial owns one new journal. Reusing a regular file would mix attempts
		// and make the durable last phase ambiguous.
		return nil, errors.New("phase journal already exists")
	} else if !errors.Is(err, fs.ErrNotExist) {
		return nil, errors.New("inspect phase journal")
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return nil, errors.New("create phase journal")
	}
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
		_ = file.Close()
		return nil, errors.New("unsafe phase journal")
	}
	return &phaseJournal{file: file}, nil
}

func (j *phaseJournal) append(phase driverPhase) error {
	if j == nil {
		return nil
	}
	if _, ok := driverPhases[phase]; !ok {
		return errors.New("unknown phase")
	}
	entry, err := json.Marshal(phaseJournalEntry{
		Version:   1,
		Phase:     phase,
		Timestamp: time.Now().UTC().Format(time.RFC3339Nano),
	})
	if err != nil {
		return errors.New("encode phase")
	}
	entry = append(entry, '\n')
	for len(entry) > 0 {
		n, writeErr := j.file.Write(entry)
		if writeErr != nil || n == 0 {
			return errors.New("append phase")
		}
		entry = entry[n:]
	}
	if err := j.file.Sync(); err != nil {
		return errors.New("sync phase")
	}
	return nil
}

func (j *phaseJournal) close() error {
	if j == nil {
		return nil
	}
	return j.file.Close()
}

// recordPhase never preserves an OS error. The result tells the adapter that
// its evidence is invalid while keeping the journal's privacy boundary intact.
func recordPhase(r *result, journal *phaseJournal, phase driverPhase) {
	if journal == nil || journal.append(phase) != nil {
		r.PhaseJournalOK = false
		r.FailureClass = "adapter"
	}
}
