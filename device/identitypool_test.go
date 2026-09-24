package device

import (
	"sync"
	"testing"
)

func TestIdentityPoolSuffixesDuplicates(t *testing.T) {
	p := NewIdentityPool()
	if got := p.Reserve("ABCD00"); got != "ABCD00" {
		t.Fatalf("first reserve = %q, want ABCD00", got)
	}
	if got := p.Reserve("ABCD00"); got != "ABCD01" {
		t.Fatalf("second reserve = %q, want ABCD01", got)
	}
	p.Release("ABCD00")
	if got := p.Reserve("ABCD00"); got != "ABCD00" {
		t.Fatalf("reserve after release = %q, want ABCD00", got)
	}
}

func TestIdentityPoolAcceptsShortDuplicates(t *testing.T) {
	p := NewIdentityPool()
	p.Reserve("A")
	if got := p.Reserve("A"); got != "A01" {
		t.Fatalf("short duplicate = %q, want A01", got)
	}
	p.Reserve("")
	if got := p.Reserve(""); got != "01" {
		t.Fatalf("empty duplicate = %q, want 01", got)
	}
}

func TestIdentityPoolIsSafeForConcurrentUse(t *testing.T) {
	p := NewIdentityPool()
	const workers = 15
	got := make(chan string, workers)
	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			got <- p.Reserve("SERIAL00")
		}()
	}
	wg.Wait()
	close(got)

	seen := map[string]bool{}
	for id := range got {
		if seen[id] {
			t.Fatalf("identity %q handed out twice", id)
		}
		seen[id] = true
	}
}
