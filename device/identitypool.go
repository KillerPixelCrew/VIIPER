package device

import (
	"fmt"
	"sync"
)

// IdentityPool hands out unique device identities such as serial numbers and
// MAC addresses across concurrently created devices of one type.
//
// Devices are created and released from separate API connection goroutines,
// so every lookup, reservation and release happens under one lock.
type IdentityPool struct {
	mu    sync.Mutex
	inUse map[string]struct{}
}

// NewIdentityPool returns an empty pool.
func NewIdentityPool() *IdentityPool {
	return &IdentityPool{inUse: map[string]struct{}{}}
}

// Reserve claims id, or a variant of it when id is already taken. A variant
// replaces the last two characters with a hex counter (01 to 0F); an id shorter
// than two characters gets the counter appended instead. When every variant is
// taken, id itself is returned and reserved again, as before this pool existed.
func (p *IdentityPool) Reserve(id string) string {
	p.mu.Lock()
	defer p.mu.Unlock()

	if _, taken := p.inUse[id]; taken {
		prefix := id
		if len(id) >= 2 {
			prefix = id[:len(id)-2]
		}
		for i := 1; i < 16; i++ {
			candidate := fmt.Sprintf("%s%02X", prefix, i)
			if _, exists := p.inUse[candidate]; !exists {
				id = candidate
				break
			}
		}
	}
	p.inUse[id] = struct{}{}
	return id
}

// Release returns id to the pool.
func (p *IdentityPool) Release(id string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.inUse, id)
}
