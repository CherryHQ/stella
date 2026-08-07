package docker

import (
	"testing"

	"github.com/CherryHQ/stella/plugins/sandbox/docker/dockerclient"
)

func TestProjectFilesystemPathAcceptsOnlyMountedCanonicalContainerPaths(t *testing.T) {
	session := &dockerSession{mountTable: []dockerclient.Mount{
		{HostPath: "/daemon/workspace", ContainerPath: "/workspace"},
		{HostPath: "/daemon/tmp", ContainerPath: "/tmp"},
	}}
	for _, input := range []string{"/workspace/a", "/tmp/a"} {
		if got, ok := session.ProjectFilesystemPath(input); !ok || got != input {
			t.Errorf("ProjectFilesystemPath(%q) = %q, %v", input, got, ok)
		}
	}
	for _, input := range []string{"/daemon/workspace/a", "/workspace-else/a", "/workspace/../tmp/a", `/workspace\a`, "/unmapped/a"} {
		if got, ok := session.ProjectFilesystemPath(input); ok {
			t.Errorf("ProjectFilesystemPath(%q) = %q, true; want rejection", input, got)
		}
	}
}

func TestFilesystemWorkingDirectoryUsesOnlyMountedContainerCoordinate(t *testing.T) {
	newSession := func(workingDir string) *dockerSession {
		return &dockerSession{workingDir: workingDir, mountTable: []dockerclient.Mount{
			{HostPath: "/daemon/workspace", ContainerPath: "/workspace"},
			{HostPath: "/daemon/tmp", ContainerPath: "/tmp"},
		}}
	}
	for _, workingDir := range []string{"/workspace/project", "/tmp/session"} {
		if got, ok := newSession(workingDir).FilesystemWorkingDirectory(); !ok || got != workingDir {
			t.Errorf("FilesystemWorkingDirectory(%q) = %q, %v", workingDir, got, ok)
		}
	}
	for _, workingDir := range []string{"/daemon/workspace", "/workspace/../tmp", "/user/project"} {
		if got, ok := newSession(workingDir).FilesystemWorkingDirectory(); ok || got != "" {
			t.Errorf("FilesystemWorkingDirectory(%q) = %q, true; want rejection", workingDir, got)
		}
	}
}
