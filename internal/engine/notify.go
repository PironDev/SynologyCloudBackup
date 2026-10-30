package engine

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
)

// synonotifyBin — утилита DSM для системных уведомлений (см. System Notification в Developer Guide).
const synonotifyBin = "/usr/syno/bin/synonotify"

// Ключи строк уведомлений; тексты лежат в ui/texts пакета (см. conf/resource).
const (
	notifyOK     = "CloudBackupJobDone"
	notifyFailed = "CloudBackupJobFailed"
)

// notifyDSM отправляет уведомление о завершении задания. Вне NAS утилиты нет — возвращает ошибку.
func notifyDSM(status, job, message string) error {
	key := notifyOK
	if status != StatusSuccess {
		key = notifyFailed
	}
	if _, err := os.Stat(synonotifyBin); err != nil {
		return errors.New("утилита synonotify недоступна")
	}
	vars, err := json.Marshal(map[string]string{"%JOB%": job, "%STATUS%": status, "%MESSAGE%": message})
	if err != nil {
		return err
	}
	return exec.Command(synonotifyBin, key, string(vars)).Run()
}
