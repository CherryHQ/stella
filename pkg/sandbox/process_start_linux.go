//go:build linux

package sandbox

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strconv"
)

// StartProcessRegistered gates the target until its kernel identity is durable.
// The gate shell is replaced by the target and therefore retains the same PID.
func StartProcessRegistered(ctx context.Context, cmd *exec.Cmd, registrar ProcessRegistrar) error {
	if registrar == nil {
		return cmd.Start()
	}
	originalPath, originalArgs := cmd.Path, append([]string(nil), cmd.Args...)
	reader, writer, err := os.Pipe()
	if err != nil {
		return err
	}
	defer func() { _ = reader.Close() }()
	gateFD := 3 + len(cmd.ExtraFiles)
	cmd.Path = "/bin/sh"
	cmd.Args = append([]string{"sh", "-c", fmt.Sprintf("IFS= read -r _ <&%d || exit $?; exec \"$@\"", gateFD), "sh", originalPath}, originalArgs[1:]...)
	cmd.ExtraFiles = append(cmd.ExtraFiles, reader)
	if err := cmd.Start(); err != nil {
		_ = writer.Close()
		return err
	}
	identity, err := LinuxProcessIdentity(cmd.Process.Pid)
	if err == nil {
		err = registrar(ctx, identity)
	}
	if err != nil {
		_ = cmd.Process.Kill()
		_ = writer.Close()
		_ = cmd.Wait()
		return fmt.Errorf("register sandbox process before launch: %w", err)
	}
	_, err = writer.Write([]byte("go\n"))
	_ = writer.Close()
	if err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return fmt.Errorf("release registered sandbox process: %w", err)
	}
	return nil
}

func LinuxProcessIdentity(pid int) (ProcessIdentity, error) {
	data, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return ProcessIdentity{}, err
	}
	end := bytes.LastIndexByte(data, ')')
	if end < 0 {
		return ProcessIdentity{}, fmt.Errorf("invalid process stat for pid %d", pid)
	}
	fields := bytes.Fields(data[end+1:])
	if len(fields) < 20 {
		return ProcessIdentity{}, fmt.Errorf("invalid process stat fields for pid %d", pid)
	}
	start, err := strconv.ParseUint(string(fields[19]), 10, 64)
	return ProcessIdentity{PID: pid, StartTime: start}, err
}
