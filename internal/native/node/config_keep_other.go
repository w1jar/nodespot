//go:build !unix

package node

import "os"

func keepOwner(*os.File, os.FileInfo) error { return nil }
