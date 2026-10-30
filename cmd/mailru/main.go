// Облако Mail.ru для Synology DSM 7.
package main

import (
	"nascloud/internal/app"
	"nascloud/internal/provider/mailru"
)

func main() {
	app.Main(app.Brand{
		ID:          "mailru",
		Title:       "Облако Mail.ru",
		Descriptor:  mailru.Descriptor(),
		WebAddr:     ":8775",
		GatewayAddr: "127.0.0.1:8776",
		ProxyAddr:   "127.0.0.1:8777",
	})
}
