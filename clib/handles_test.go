package main

import "testing"

func TestReleasedHandleIsRefusedAndItsSlotReusedUnderANewGeneration(t *testing.T) {
	var table handleTable[*int]
	first, second := new(int), new(int)

	stale, err := table.open(first)
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := table.get(stale); !ok || got != first {
		t.Fatalf("open handle resolved to %v, %v", got, ok)
	}

	table.release(first)
	if _, ok := table.get(stale); ok {
		t.Fatal("a released handle still resolved")
	}

	fresh, err := table.open(second)
	if err != nil {
		t.Fatal(err)
	}
	if fresh&handleIndexMask != stale&handleIndexMask {
		t.Fatalf("freed slot was not reused: %#x then %#x", stale, fresh)
	}
	if fresh == stale {
		t.Fatal("a reused slot kept its old generation")
	}
	if _, ok := table.get(stale); ok {
		t.Fatal("an old handle reached the device that reused its slot")
	}
	if got, ok := table.get(fresh); !ok || got != second {
		t.Fatalf("new handle resolved to %v, %v", got, ok)
	}
}

func TestUnknownHandlesAreRefused(t *testing.T) {
	var table handleTable[*int]
	for _, handle := range []uint32{0, 1, 1 << handleIndexBits, 0xFFFFFFFF} {
		if _, ok := table.get(handle); ok {
			t.Fatalf("handle %#x resolved in an empty table", handle)
		}
	}
	table.clear()
	if _, ok := table.get(1); ok {
		t.Fatal("handle resolved after clear")
	}
}
