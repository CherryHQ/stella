package docker

// Docker Desktop mediates Windows filesystem ownership; keep the image user.
func dockerProcessUser(bool, DockerSandboxMode) string { return "" }
