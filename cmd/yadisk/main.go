// Яндекс.Диск для Synology DSM 7.
package main

import (
	"nascloud/internal/app"
	"nascloud/internal/provider/yandex"
)

func main() {
	app.Main(app.Brand{
		ID:          "yadisk",
		Title:       "Яндекс.Диск",
		Descriptor:  yandex.Descriptor(),
		WebAddr:     ":8765",
		GatewayAddr: "127.0.0.1:8766",
		ProxyAddr:   "127.0.0.1:8767",
	})
}
