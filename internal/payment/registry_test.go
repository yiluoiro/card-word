package payment

import (
	"sync"
	"testing"
)

func TestRegistryConcurrentReload(t *testing.T) {
	r := NewRegistry()
	var wg sync.WaitGroup
	for range 12 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 100 {
				r.Register(&Manual{})
				r.List()
				r.Get("manual")
				r.Unregister("manual")
			}
		}()
	}
	wg.Wait()
	r.Register(&Manual{})
	if len(r.List()) != 1 {
		t.Fatal("registry lost consistent ordering")
	}
}
