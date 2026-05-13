package resources

import (
	"io/fs"
	"path"
)

type skillRoot struct {
	Path string
	Leaf string
}

func listSkillRoots(root fs.FS) ([]skillRoot, error) {
	var roots []skillRoot
	err := fs.WalkDir(root, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() || p == "." {
			return nil
		}
		if _, err := fs.Stat(root, path.Join(p, "SKILL.md")); err == nil {
			roots = append(roots, skillRoot{Path: p, Leaf: path.Base(p)})
			return fs.SkipDir
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return roots, nil
}
