package store_test

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/yiluoiro/card-word/internal/store"
)

func TestInstallCreatesOnlyOneOwnerConcurrently(t *testing.T) {
	db := newDB(t)
	var successes atomic.Int32
	var wg sync.WaitGroup
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := db.Install(context.Background(), store.Installation{SiteName: "Store", Username: "owner", Password: "install-long-password"}); err == nil {
				successes.Add(1)
			}
		}()
	}
	wg.Wait()
	if successes.Load() != 1 {
		t.Fatalf("successful installations: %d", successes.Load())
	}
	if n, err := db.CountOwners(context.Background()); err != nil || n != 1 {
		t.Fatalf("owners %d: %v", n, err)
	}
}
