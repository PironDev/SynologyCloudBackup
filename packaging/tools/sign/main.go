// Command sign создаёт ключ подписи ed25519 и подписывает файлы релиза.
//
//	sign keygen <файл-ключа>       пишет приватный ключ (base64) и печатает публичный (base64)
//	sign sign <файл-ключа> <файл>  пишет <файл>.sig (сырые байты подписи)
package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"os"
	"strings"
)

func main() {
	if len(os.Args) < 3 {
		usage()
	}
	switch {
	case os.Args[1] == "keygen" && len(os.Args) == 3:
		pub, priv, err := ed25519.GenerateKey(rand.Reader)
		check(err)
		check(os.WriteFile(os.Args[2], []byte(base64.StdEncoding.EncodeToString(priv)+"\n"), 0o600))
		fmt.Println(base64.StdEncoding.EncodeToString(pub))
	case os.Args[1] == "sign" && len(os.Args) == 4:
		raw, err := os.ReadFile(os.Args[2])
		check(err)
		priv, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(raw)))
		if err != nil || len(priv) != ed25519.PrivateKeySize {
			check(fmt.Errorf("неверный файл приватного ключа %s", os.Args[2]))
		}
		data, err := os.ReadFile(os.Args[3])
		check(err)
		check(os.WriteFile(os.Args[3]+".sig", ed25519.Sign(priv, data), 0o644))
	default:
		usage()
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "использование: sign keygen <файл-ключа> | sign sign <файл-ключа> <файл>")
	os.Exit(2)
}

func check(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
