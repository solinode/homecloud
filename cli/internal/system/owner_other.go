//go:build !unix

package system

import "os"

func matchOwner(string, os.FileInfo) {}
