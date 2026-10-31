package rclone

import (
	"bytes"
	"context"
	"io"
	"net/http/httptest"
	"testing"

	"github.com/rclone/gofakes3"
	"github.com/rclone/gofakes3/s3mem"

	"nascloud/internal/provider"
)

// Проверка S3 на локальном сервере gofakes3: тот же протокол, что у Amazon S3.
func TestS3Driver(t *testing.T) {
	ctx := context.Background()
	be := s3mem.New()
	if err := be.CreateBucket(ctx, "bkt"); err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(gofakes3.New(be).Server())
	defer ts.Close()

	p, err := New("s3", map[string]string{
		"provider": "Other", "access_key_id": "key", "secret_access_key": "secret",
		"region": "us-east-1", "endpoint": ts.URL, "root": "bkt/backups",
	})
	if err != nil {
		t.Fatal(err)
	}
	data := []byte("s3 payload")
	src := provider.Source{Size: int64(len(data)), Open: func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(data)), nil }}
	if err := p.Upload(ctx, "/dir/file.bin", src); err != nil {
		t.Fatal(err)
	}
	st, err := p.Stat(ctx, "/dir/file.bin")
	if err != nil || st.Size != int64(len(data)) {
		t.Fatalf("Stat = %+v, %v", st, err)
	}
	if d, err := p.Stat(ctx, "/dir"); err != nil || !d.IsDir {
		t.Fatalf("Stat папки без маркера = %+v, %v", d, err)
	}
	rc, err := p.Download(ctx, "/dir/file.bin", 0, -1)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(rc)
	rc.Close()
	if !bytes.Equal(got, data) {
		t.Fatalf("download = %q, want %q", got, data)
	}
	if err := p.Delete(ctx, "/dir"); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Stat(ctx, "/dir/file.bin"); err == nil {
		t.Fatal("файл остался после удаления папки")
	}
}
