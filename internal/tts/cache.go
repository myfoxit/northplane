package tts

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Cache keeps synthesized clips on disk, keyed by a hash of everything
// that influences the audio (engine + config, voice, rate, normalised
// text, post-processing). IVR prompts, menu options and repeated
// announcements are rendered once; cloud engines bill per character and
// a local piper takes seconds per sentence, so the cache is what makes
// phone menus snappy. Eviction: least recently used when the size cap
// is exceeded, plus a TTL sweep.
type Cache struct {
	dir      string
	maxBytes int64
	ttl      time.Duration

	mu   sync.Mutex
	size int64
}

// NewCache opens (creating) dir. maxMB ≤ 0 → 256, ttl ≤ 0 → 7 days.
func NewCache(dir string, maxMB int, ttl time.Duration) (*Cache, error) {
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, err
	}
	if maxMB <= 0 {
		maxMB = 256
	}
	if ttl <= 0 {
		ttl = 7 * 24 * time.Hour
	}
	c := &Cache{dir: dir, maxBytes: int64(maxMB) << 20, ttl: ttl}
	c.size = c.scan()
	return c, nil
}

// Dir returns the cache directory.
func (c *Cache) Dir() string { return c.dir }

// ID derives the cache id for a set of key parts.
func ID(parts ...string) string {
	h := sha256.Sum256([]byte(strings.Join(parts, "\x1f")))
	return hex.EncodeToString(h[:16])
}

// Path of the clip file for id (exists or not).
func (c *Cache) Path(id string) string {
	return filepath.Join(c.dir, id+".wav")
}

// Get returns the file path of a cached clip and marks it used.
func (c *Cache) Get(id string) (string, bool) {
	if !validID(id) {
		return "", false
	}
	p := c.Path(id)
	if st, err := os.Stat(p); err != nil || st.IsDir() {
		return "", false
	}
	now := time.Now()
	_ = os.Chtimes(p, now, now) // LRU bookkeeping via mtime
	return p, true
}

// Put stores a clip (atomic rename) and evicts when over the cap.
func (c *Cache) Put(id string, data []byte) (string, error) {
	p := c.Path(id)
	tmp, err := os.CreateTemp(c.dir, ".tmp-*")
	if err != nil {
		return "", err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
		return "", err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmp.Name())
		return "", err
	}
	if err := os.Rename(tmp.Name(), p); err != nil {
		_ = os.Remove(tmp.Name())
		return "", err
	}
	c.mu.Lock()
	c.size += int64(len(data))
	over := c.size > c.maxBytes
	c.mu.Unlock()
	if over {
		c.Sweep()
	}
	return p, nil
}

// Sweep removes expired clips and, if still over the cap, the least
// recently used ones down to 90 % of the cap. Safe to call periodically.
func (c *Cache) Sweep() {
	c.mu.Lock()
	defer c.mu.Unlock()
	entries, err := os.ReadDir(c.dir)
	if err != nil {
		return
	}
	type ent struct {
		path string
		mod  time.Time
		size int64
	}
	var files []ent
	var total int64
	cutoff := time.Now().Add(-c.ttl)
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".wav") {
			if strings.HasPrefix(e.Name(), ".tmp-") {
				if info, err := e.Info(); err == nil && info.ModTime().Before(time.Now().Add(-time.Hour)) {
					_ = os.Remove(filepath.Join(c.dir, e.Name()))
				}
			}
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		p := filepath.Join(c.dir, e.Name())
		if info.ModTime().Before(cutoff) {
			_ = os.Remove(p)
			continue
		}
		files = append(files, ent{path: p, mod: info.ModTime(), size: info.Size()})
		total += info.Size()
	}
	if total > c.maxBytes {
		sort.Slice(files, func(i, j int) bool { return files[i].mod.Before(files[j].mod) })
		target := c.maxBytes * 9 / 10
		for _, f := range files {
			if total <= target {
				break
			}
			if err := os.Remove(f.path); err == nil {
				total -= f.size
			}
		}
	}
	c.size = total
}

// scan sums the current cache size.
func (c *Cache) scan() int64 {
	var total int64
	entries, err := os.ReadDir(c.dir)
	if err != nil {
		return 0
	}
	for _, e := range entries {
		if info, err := e.Info(); err == nil && !e.IsDir() {
			total += info.Size()
		}
	}
	return total
}

// Size returns the tracked byte count.
func (c *Cache) Size() int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.size
}

func validID(id string) bool {
	if len(id) != 32 {
		return false
	}
	for _, r := range id {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return false
		}
	}
	return true
}
