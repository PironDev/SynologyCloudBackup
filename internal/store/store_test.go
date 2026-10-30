package store

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"nascloud/internal/secret"
)

func TestStoreEncryptsSecretsAndRoundTrips(t *testing.T) {
	dir := t.TempDir()
	box, err := secret.LoadOrCreate(filepath.Join(dir, "secret.key"))
	if err != nil {
		t.Fatal(err)
	}
	st, err := Open(dir, box, []string{"token"})
	if err != nil {
		t.Fatal(err)
	}
	err = st.Update(func(c *Config) error {
		c.Accounts = append(c.Accounts, Account{ID: "a1", Name: "main", Slug: "main", Fields: map[string]string{"token": "SUPER-SECRET-TOKEN", "login": "user@example.com"}})
		c.PasswordHash = "hash"
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	raw, _ := os.ReadFile(filepath.Join(dir, "config.json"))
	if strings.Contains(string(raw), "SUPER-SECRET-TOKEN") {
		t.Fatal("секрет лежит на диске открытым текстом")
	}
	if !strings.Contains(string(raw), "user@example.com") {
		t.Fatal("несекретное поле должно остаться читаемым")
	}
	for _, f := range []string{"config.json", "secret.key"} {
		if fi, _ := os.Stat(filepath.Join(dir, f)); fi.Mode().Perm() != 0o600 {
			t.Errorf("%s: права %v, ожидалось 0600", f, fi.Mode().Perm())
		}
	}

	st2, err := Open(dir, box, []string{"token"})
	if err != nil {
		t.Fatal(err)
	}
	a, ok := st2.Get().Account("a1")
	if !ok || a.Fields["token"] != "SUPER-SECRET-TOKEN" || st2.Get().PasswordHash != "hash" {
		t.Fatalf("после перезагрузки данные не совпали: %+v", a)
	}

	other, _ := secret.New(make([]byte, 32))
	if _, err := Open(dir, other, []string{"token"}); err == nil {
		t.Fatal("с чужим ключом конфиг не должен открываться")
	}
}

func TestUpdateRollsBackOnError(t *testing.T) {
	dir := t.TempDir()
	box, _ := secret.LoadOrCreate(filepath.Join(dir, "secret.key"))
	st, _ := Open(dir, box, nil)
	_ = st.Update(func(c *Config) error { c.Jobs = []Job{{ID: "j", Name: "one"}}; return nil })
	err := st.Update(func(c *Config) error { c.Jobs = nil; return os.ErrInvalid })
	if err == nil || len(st.Get().Jobs) != 1 {
		t.Fatal("при ошибке изменение не должно применяться")
	}
}

func TestSecretBox(t *testing.T) {
	box, _ := secret.New(make([]byte, 32))
	enc := box.Seal("password")
	if enc == "password" || !strings.HasPrefix(enc, "enc1:") {
		t.Fatalf("Seal: %q", enc)
	}
	if box.Seal(enc) != enc {
		t.Fatal("повторный Seal не должен шифровать дважды")
	}
	if got, err := box.Open(enc); err != nil || got != "password" {
		t.Fatalf("Open: %q, %v", got, err)
	}
	if got, _ := box.Open("plain"); got != "plain" {
		t.Fatal("значение без префикса возвращается как есть")
	}
	if box.Seal("") != "" {
		t.Fatal("пустая строка остаётся пустой")
	}
}
