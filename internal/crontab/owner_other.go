//go:build !unix

package crontab

import "os"

func copyOwner(os.FileInfo, string) error { return nil }
