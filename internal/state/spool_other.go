//go:build !linux

package state

import "os"

// The host agent and its authenticated spooling contract are Linux-only.
func openSpoolRegular(path string) (*os.File, error) { return nil, ErrSpoolInvalid }
func spoolAvailableBytes(path string) (int64, error) { return 0, ErrSpoolInvalid }
func volatileSpoolRoot(path string) (string, error) { return "", ErrSpoolInvalid }

func spoolDirectoryOwned(info os.FileInfo) bool { return false }
