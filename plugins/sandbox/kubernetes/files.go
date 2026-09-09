package kubernetes

import (
	"errors"
	"io/fs"

	sandbox "github.com/CherryHQ/stella/pkg/sandbox"
)

// A selected file view must never follow a replaced source root or outlive its
// execution generation, including while termination is waiting for the API.
type guardedFiles struct{ s *session }

func (f guardedFiles) check(fn func() error) error {
	f.s.mu.Lock()
	defer f.s.mu.Unlock()
	if f.s.closed || f.s.invalid {
		return errors.New("kubernetes: filesystem generation is invalid")
	}
	if err := f.s.resolver.ValidateBackingPaths(); err != nil {
		return err
	}
	return fn()
}

func (f guardedFiles) ReadFile(p string) (out []byte, err error) {
	err = f.check(func() error { out, err = f.s.files.ReadFile(p); return err })
	return
}

func (f guardedFiles) ReadDir(p string) (out []sandbox.DirEntry, err error) {
	err = f.check(func() error { out, err = f.s.files.ReadDir(p); return err })
	return
}

func (f guardedFiles) Stat(p string) (out sandbox.FileInfo, err error) {
	err = f.check(func() error { out, err = f.s.files.Stat(p); return err })
	return
}

func (f guardedFiles) WriteFile(p string, b []byte, m fs.FileMode) error {
	return f.check(func() error { return f.s.files.WriteFile(p, b, m) })
}

func (f guardedFiles) ProjectFiles(p string, b []sandbox.ProjectedFile) error {
	return f.check(func() error { return f.s.files.ProjectFiles(p, b) })
}

func (f guardedFiles) ProjectTempFiles(p string, b []sandbox.ProjectedFile) (out string, err error) {
	err = f.check(func() error { out, err = f.s.files.ProjectTempFiles(p, b); return err })
	return
}
