package library

import "sync"

// pathLocks serialises writes to individual files.
//
// Tag writing is a read-modify-write: the existing tag is parsed, altered and
// written back. Two clients editing the same track at once would interleave
// those steps and one edit would vanish. Locking per path rather than globally
// means a batch across ten thousand files still runs concurrently.
//
// Locks are sharded by hash rather than allocated per path, so the structure
// does not grow with the library and never needs cleaning up. Two unrelated
// paths sharing a shard occasionally wait on each other, which costs nothing
// measurable next to the file IO they are about to do.
type pathLocks struct {
	shards [pathLockShards]sync.Mutex
}

const pathLockShards = 256

func (p *pathLocks) Lock(path string) {
	p.shards[hash64(path)%pathLockShards].Lock()
}

func (p *pathLocks) Unlock(path string) {
	p.shards[hash64(path)%pathLockShards].Unlock()
}

// withPath runs fn while holding the lock for one path.
func (p *pathLocks) withPath(path string, fn func() error) error {
	p.Lock(path)
	defer p.Unlock(path)
	return fn()
}
