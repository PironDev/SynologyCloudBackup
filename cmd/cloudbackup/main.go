// CloudBackup для Synology DSM 7: Яндекс.Диск и Облако Mail.ru в одном пакете.
package main

import (
	"nascloud/internal/app"
	"nascloud/internal/provider"
	"nascloud/internal/provider/mailru"
	"nascloud/internal/provider/rclone"
	"nascloud/internal/provider/yandex"
)

func main() {
	app.Main(app.Brand{
		ID:          "cloudbackup",
		Title:       "CloudBackup",
		Descriptors: append([]provider.Descriptor{yandex.Descriptor(), mailru.Descriptor()}, rclone.Descriptors()...),
		WebAddr:     ":8765",
		GatewayAddr: "127.0.0.1:8766",
		ProxyAddr:   "127.0.0.1:8767",
	})
}
