// Package updater ищет в релизах GitHub более новую версию пакета и проверяет её подпись.
package updater

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
)

// Release — найденная новая версия.
type Release struct {
	Version string // без префикса v
	URL     string // страница релиза для ручной установки
}

// Newer сообщает, что версия a новее b. Сравнивает три числа через точку.
// shortcut: суффиксы (-beta) не учитываются, GitHub /releases/latest их не отдаёт; upgrade — полный semver.
func Newer(a, b string) bool {
	pa, pb := parts(a), parts(b)
	for i := range pa {
		if pa[i] != pb[i] {
			return pa[i] > pb[i]
		}
	}
	return false
}

func parts(v string) [3]int {
	var out [3]int
	v = strings.TrimPrefix(strings.TrimSpace(v), "v")
	if i := strings.IndexAny(v, "-+"); i >= 0 {
		v = v[:i]
	}
	for i, s := range strings.SplitN(v, ".", 3) {
		out[i], _ = strconv.Atoi(s)
	}
	return out
}

// Check возвращает новый релиз, если его подпись верна. Без репозитория или без новой версии — ok=false.
// Подпись проверяется по SHA256SUMS; ассет пакета должен называться pkg-<версия>.spk.
func Check(ctx context.Context, c *http.Client, api, repo, pubKey, pkg, current string) (rel Release, ok bool, err error) {
	if repo == "" {
		return Release{}, false, nil
	}
	var r struct {
		Tag    string `json:"tag_name"`
		URL    string `json:"html_url"`
		Assets []struct {
			Name string `json:"name"`
			URL  string `json:"browser_download_url"`
		} `json:"assets"`
	}
	req, err := http.NewRequestWithContext(ctx, "GET", api+"/repos/"+repo+"/releases/latest", nil)
	if err != nil {
		return Release{}, false, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	res, err := c.Do(req)
	if err != nil {
		return Release{}, false, err
	}
	defer res.Body.Close()
	if res.StatusCode == http.StatusNotFound {
		return Release{}, false, nil // опубликованных релизов нет
	}
	if res.StatusCode != http.StatusOK {
		return Release{}, false, fmt.Errorf("GitHub ответил %s", res.Status)
	}
	if err := json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(&r); err != nil {
		return Release{}, false, err
	}
	ver := strings.TrimPrefix(r.Tag, "v")
	if !Newer(ver, current) {
		return Release{}, false, nil
	}
	assets := map[string]string{}
	for _, a := range r.Assets {
		assets[a.Name] = a.URL
	}
	if _, ok := assets[pkg+"-"+ver+".spk"]; !ok {
		return Release{}, false, fmt.Errorf("в релизе %s нет пакета %s", ver, pkg)
	}
	sums, sig := assets["SHA256SUMS"], assets["SHA256SUMS.sig"]
	if sums == "" || sig == "" {
		return Release{}, false, errors.New("в релизе нет SHA256SUMS или его подписи")
	}
	sumsData, err := fetch(ctx, c, sums)
	if err != nil {
		return Release{}, false, err
	}
	sigData, err := fetch(ctx, c, sig)
	if err != nil {
		return Release{}, false, err
	}
	if err := verify(pubKey, sumsData, sigData); err != nil {
		return Release{}, false, err
	}
	return Release{Version: ver, URL: r.URL}, true, nil
}

// verify проверяет подпись ed25519; ключ и подпись приходят в виде base64 (подпись — сырые байты).
func verify(pubKey string, data, sig []byte) error {
	pub, err := base64.StdEncoding.DecodeString(pubKey)
	if err != nil || len(pub) != ed25519.PublicKeySize {
		return errors.New("неверный публичный ключ обновлений")
	}
	if !ed25519.Verify(pub, data, sig) {
		return errors.New("подпись релиза не сходится: обновление не показано")
	}
	return nil
}

func fetch(ctx context.Context, c *http.Client, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}
	res, err := c.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("загрузка %s: %s", url, res.Status)
	}
	return io.ReadAll(io.LimitReader(res.Body, 1<<16))
}
