//go:build unix

package local

import "syscall"

// DiskUsage возвращает размер тома и свободное место для пользователя.
func DiskUsage(p string) (total, free int64, err error) {
	var st syscall.Statfs_t
	if err = syscall.Statfs(p, &st); err != nil {
		return 0, 0, err
	}
	return int64(st.Blocks) * int64(st.Bsize), int64(st.Bavail) * int64(st.Bsize), nil
}

func deviceID(p string) (uint64, bool) {
	var st syscall.Stat_t
	if err := syscall.Stat(p, &st); err != nil {
		return 0, false
	}
	return uint64(st.Dev), true
}
