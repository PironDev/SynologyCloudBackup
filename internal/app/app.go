// Package app собирает вендорскую сборку: хранилище, движок, шлюз и веб-интерфейс.
package app

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	_ "time/tzdata" // часовой пояс, если на NAS нет /usr/share/zoneinfo

	"nascloud/internal/auth"
	"nascloud/internal/dsm"
	"nascloud/internal/engine"
	"nascloud/internal/gateway"
	"nascloud/internal/provider"
	"nascloud/internal/secret"
	"nascloud/internal/store"
	"nascloud/internal/telemetry"
	"nascloud/internal/version"
	"nascloud/internal/web"
)

// Brand — всё, что отличает одну вендорскую сборку от другой.
type Brand struct {
	ID          string
	Title       string
	Descriptors []provider.Descriptor // провайдеры пакета; вид аккаунта выбирает нужный
	WebAddr     string                // прямой интерфейс с паролем (необязателен), по умолчанию ":8765"
	GatewayAddr string                // по умолчанию "127.0.0.1:8766"
	ProxyAddr   string                // внутренний интерфейс для CGI-моста DSM, по умолчанию "127.0.0.1:8767"
}

func isLoopback(addr string) bool {
	h, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	ip := net.ParseIP(h)
	return h == "localhost" || (ip != nil && ip.IsLoopback())
}

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

// Main — точка входа вендорской сборки.
func Main(b Brand) {
	dataDir := flag.String("data", env("CB_DATA", "./data"), "каталог данных (config.json, журналы)")
	webAddr := flag.String("web", env("CB_WEB_ADDR", b.WebAddr), "адрес веб-интерфейса")
	gwAddr := flag.String("gateway", env("CB_GATEWAY_ADDR", b.GatewayAddr), "адрес WebDAV-шлюза для File Station")
	proxyAddr := flag.String("proxy", env("CB_PROXY_ADDR", b.ProxyAddr), "адрес внутреннего интерфейса для CGI-моста DSM (только localhost)")
	showVersion := flag.Bool("version", false, "показать версию")
	cgiMode := flag.Bool("cgi", false, "работать как CGI-мост DSM (запускается веб-сервером DSM)")
	allow := flag.String("allow-users", "", "CGI: пользователи DSM, которым разрешён доступ кроме администраторов (через запятую, * — любой)")
	authProg := flag.String("dsm-auth", dsm.DefaultAuthProgram, "CGI: программа проверки сессии DSM")
	groupFile := flag.String("group-file", dsm.DefaultGroupFile, "CGI: файл групп")
	flag.Parse()
	if *showVersion {
		fmt.Println(b.Title, version.Version)
		return
	}
	if *cgiMode {
		a := dsm.Auth{Program: *authProg, GroupFile: *groupFile}
		for _, u := range strings.Split(*allow, ",") {
			if u = strings.TrimSpace(u); u != "" {
				a.Allow = append(a.Allow, u)
			}
		}
		dsm.CGI{DataDir: *dataDir, Auth: a, Base: "index.cgi?r="}.Run(os.Environ(), os.Stdin, os.Stdout)
		return
	}
	log.SetFlags(log.LstdFlags)
	if err := run(b, *dataDir, *webAddr, *gwAddr, *proxyAddr); err != nil {
		log.Fatalf("ошибка: %v", err)
	}
}

func run(b Brand, dataDir, webAddr, gwAddr, proxyAddr string) error {
	dataDir, err := filepath.Abs(dataDir)
	if err != nil {
		return err
	}
	box, err := secret.LoadOrCreate(filepath.Join(dataDir, "secret.key"))
	if err != nil {
		return err
	}
	var keys []string
	for _, d := range b.Descriptors {
		keys = append(keys, d.SecretKeys()...)
	}
	st, err := store.Open(dataDir, box, keys)
	if err != nil {
		return err
	}
	if st.Get().Settings.InstallID == "" {
		_ = st.Update(func(c *store.Config) error { c.Settings.InstallID = auth.RandomToken(8); return nil })
	}
	telemetry.Init(b.ID, version.Version,
		func() bool { t := st.Get().Settings.Telemetry; return t != nil && *t },
		func() string { return st.Get().Settings.InstallID })
	pool := engine.NewPool(b.Descriptors...)
	mgr, err := engine.NewManager(st, pool, dataDir)
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	go mgr.RunScheduler(ctx)

	webSrv := web.New(web.Deps{Title: b.Title, Store: st, Mgr: mgr, Pool: pool, GatewayAddr: gwAddr, Version: version.Version})
	gw := &gateway.Server{Store: st, Pool: pool, TmpDir: mgr.TmpDir}

	servers := []*http.Server{{Addr: gwAddr, Handler: gw, ReadHeaderTimeout: 15 * time.Second}}
	if webAddr != "" { // прямой доступ по паролю — необязателен
		servers = append(servers, &http.Server{Addr: webAddr, Handler: webSrv.Handler(), ReadHeaderTimeout: 15 * time.Second})
	}
	if proxyAddr != "" { // вход через учётную запись DSM
		if !isLoopback(proxyAddr) {
			return fmt.Errorf("адрес для CGI-моста должен быть локальным (127.0.0.1), получено %q", proxyAddr)
		}
		secret := auth.RandomToken(24)
		if err := dsm.WriteProxyInfo(dataDir, dsm.ProxyInfo{Addr: proxyAddr, Secret: secret}); err != nil {
			return err
		}
		servers = append(servers, &http.Server{Addr: proxyAddr, Handler: webSrv.ProxyHandler(secret), ReadHeaderTimeout: 15 * time.Second})
	}
	errc := make(chan error, len(servers))
	for _, s := range servers {
		s := s
		go func() {
			log.Printf("%s %s: слушаю %s", b.Title, version.Version, s.Addr)
			if err := s.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				errc <- fmt.Errorf("%s: %w", s.Addr, err)
			}
		}()
	}
	var result error
	select {
	case <-ctx.Done():
		log.Printf("остановка…")
	case result = <-errc:
	}
	mgr.Shutdown(15 * time.Second)
	sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for _, s := range servers {
		_ = s.Shutdown(sctx)
	}
	return result
}
