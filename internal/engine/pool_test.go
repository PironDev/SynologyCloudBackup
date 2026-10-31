package engine

import (
	"testing"

	"nascloud/internal/provider"
)

func TestPoolDescByKind(t *testing.T) {
	p := NewPool(provider.Descriptor{Kind: "yandex"}, provider.Descriptor{Kind: "mailru"})
	if d, err := p.Desc("mailru"); err != nil || d.Kind != "mailru" {
		t.Fatalf("Desc(mailru) = %q, %v", d.Kind, err)
	}
	if _, err := p.Desc(""); err == nil {
		t.Fatal("пустой вид при двух провайдерах должен быть ошибкой")
	}
	if _, err := p.Desc("dropbox"); err == nil {
		t.Fatal("неизвестный вид должен быть ошибкой")
	}
	single := NewPool(provider.Descriptor{Kind: "yandex"})
	if d, err := single.Desc(""); err != nil || d.Kind != "yandex" {
		t.Fatalf("один провайдер без вида: %q, %v", d.Kind, err)
	}
}
