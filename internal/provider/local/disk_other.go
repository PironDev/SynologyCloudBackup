//go:build !unix

package local

import "errors"

func DiskUsage(p string) (int64, int64, error) {
	return 0, 0, errors.New("не поддерживается")
}
func deviceID(p string) (uint64, bool) { return 0, false }
