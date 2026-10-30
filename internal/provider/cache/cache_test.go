package cache

import (
	"context"
	"testing"
	"time"

	"nascloud/internal/provider"
	"nascloud/internal/testutil"
)

type counting struct {
	*testutil.Mem
	lists int
}

func (c *counting) List(ctx context.Context, d string) ([]provider.Entry, error) {
	c.lists++
	return c.Mem.List(ctx, d)
}

func TestCache(t *testing.T) {
	ctx := context.Background()
	c := &counting{Mem: testutil.NewMem()}
	f := Wrap(c, time.Minute)
	f.List(ctx, "/")
	f.List(ctx, "/")
	if c.lists != 1 {
		t.Fatalf("списков %d", c.lists)
	}
	f.Mkdir(ctx, "/a")
	es, _ := f.List(ctx, "/")
	if c.lists != 2 || len(es) != 1 {
		t.Fatalf("после mkdir: списков %d, записей %d", c.lists, len(es))
	}
	f.ListFresh(ctx, "/")
	if c.lists != 3 {
		t.Fatal("ListFresh должен читать заново")
	}
}
