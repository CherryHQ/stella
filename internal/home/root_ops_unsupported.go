//go:build !unix

package home

import "os"

func symlinkRoot(*os.Root, string, string) error         { return errUnsupported }
func readlinkRoot(*os.Root, string) (string, error)      { return "", errUnsupported }
func renameRootNoReplace(*os.Root, string, string) error { return errUnsupported }
