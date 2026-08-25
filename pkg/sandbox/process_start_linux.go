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

// LinuxProcessIdentity is the durable kernel identity of pid: the PID alone is
// recycled, the start time makes it unambiguous.
func LinuxProcessIdentity(pid int) (ProcessIdentity, error) {
	identity, _, _, err := LinuxProcessStat(pid)
	return identity, err
}

// LinuxProcessStat reads the identity, the parent PID and the state character
// of pid from /proc. Parsing lives here alone because the field offsets are
// easy to get subtly wrong: the comm field can contain spaces and parentheses,
// so everything is counted from the last ')'.
func LinuxProcessStat(pid int) (ProcessIdentity, int, byte, error) {
	data, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return ProcessIdentity{}, 0, 0, err
	}
	end := bytes.LastIndexByte(data, ')')
	if end < 0 {
		return ProcessIdentity{}, 0, 0, fmt.Errorf("invalid process stat for pid %d", pid)
	}
	fields := bytes.Fields(data[end+1:])
	if len(fields) < 20 || len(fields[0]) != 1 {
		return ProcessIdentity{}, 0, 0, fmt.Errorf("invalid process stat fields for pid %d", pid)
	}
	parent, err := strconv.Atoi(string(fields[1]))
	if err != nil {
		return ProcessIdentity{}, 0, 0, err
	}
	start, err := strconv.ParseUint(string(fields[19]), 10, 64)
	if err != nil {
		return ProcessIdentity{}, 0, 0, err
	}
	return ProcessIdentity{PID: pid, StartTime: start}, parent, fields[0][0], nil
}
