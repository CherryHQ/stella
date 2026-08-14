package prompt

import "github.com/CherryHQ/stella/pkg/sandbox"

func readPromptFile(session sandbox.Session, path string) (string, bool) {
	content, err := session.Files().ReadFile(path)
	if err != nil {
		return "", false
	}
	return string(content), true
}

func statPromptFile(session sandbox.Session, path string) (string, bool) {
	info, err := session.Files().Stat(path)
	if err != nil || info.IsDir {
		return "", false
	}
	return path, true
}

func readPromptDir(session sandbox.Session, path string) ([]sandbox.DirEntry, error) {
	return session.Files().ReadDir(path)
}
