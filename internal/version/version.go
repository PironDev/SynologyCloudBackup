// Package version хранит версию сборки (подставляется через -ldflags).
package version

// Version — версия пакета.
var Version = "1.0.0"

// Repo — репозиторий GitHub «владелец/имя» для проверки обновлений; пусто — проверка выключена.
var Repo = ""

// PubKey — публичный ключ ed25519 в base64; им проверяется подпись релиза.
var PubKey = ""

// Pkg — имя пакета в релизе, например YaDiskBackup.
var Pkg = ""
