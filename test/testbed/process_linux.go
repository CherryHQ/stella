//go:build linux

package main

import (
	"io/fs"
	"os"
	"strconv"
	"strings"
)

func identityFor(pid int) (processIdentity, error) {
	data, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if os.IsNotExist(err) {
		return processIdentity{}, fs.ErrNotExist
	}
	if err != nil {
		return processIdentity{}, err
	}
	end := strings.LastIndex(string(data), ")")
	if end < 0 {
		return processIdentity{}, fs.ErrNotExist
	}
	fields := strings.Fields(string(data)[end+1:])
	if len(fields) < 20 {
		return processIdentity{}, fs.ErrNotExist
	}
	return processIdentity{PID: pid, Started: fields[19]}, nil
}
