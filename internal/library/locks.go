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

// withPaths runs fn while holding the locks for two paths, which a move needs:
// it reads one file and creates another, and both ends have to be still.
//
// The locks are taken in shard order so that two moves crossing each other —
// one renaming A to B while another renames B to A — cannot each hold what the
// other waits for. When both paths land in the same shard, one lock already
// covers both and taking it twice would deadlock against itself.
func (p *pathLocks) withPaths(a, b string, fn func() error) error {
	i := hash64(a) % pathLockShards
	j := hash64(b) % pathLockShards
	if i == j {
		p.shards[i].Lock()
		defer p.shards[i].Unlock()
		return fn()
	}
	if i > j {
		i, j = j, i
	}
	p.shards[i].Lock()
	defer p.shards[i].Unlock()
	p.shards[j].Lock()
	defer p.shards[j].Unlock()
	return fn()
}
