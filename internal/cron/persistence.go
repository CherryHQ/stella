package cron

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// jobsFile returns the path to the jobs JSON file.
func (s *Service) jobsFile() string {
	return filepath.Join(s.dataPath, "jobs.json")
}

// loadJobs reads the persisted jobs from disk.
func (s *Service) loadJobs() ([]Job, error) {
	data, err := os.ReadFile(s.jobsFile())
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	var jobs []Job
	if err := json.Unmarshal(data, &jobs); err != nil {
		return nil, fmt.Errorf("parse jobs.json: %w", err)
	}
	return jobs, nil
}

// saveJobsLocked writes all jobs to disk atomically. Caller must hold s.mu.
func (s *Service) saveJobsLocked() error {
	if err := os.MkdirAll(s.dataPath, 0o755); err != nil {
		return err
	}

	jobs := make([]Job, 0, len(s.jobs))
	for _, j := range s.jobs {
		jobs = append(jobs, j)
	}

	data, err := json.MarshalIndent(jobs, "", "  ")
	if err != nil {
		return err
	}

	tmp := s.jobsFile() + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, s.jobsFile())
}
