package state

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
)

type Entry struct {
	Size  int64  `json:"size"`
	SHA1  string `json:"sha1"`
	MTime int64  `json:"mtime"`
}

// Cache is a JSON-on-disk record of verified files for resume.
// Keyed by "{depotId}_{manifestId}/{filepath}". Thread-safe.
type Cache struct {
	path  string
	data  map[string]Entry
	dirty bool
	mu    sync.Mutex
}

func New(path string) *Cache {
	c := &Cache{path: path, data: make(map[string]Entry)}
	b, err := os.ReadFile(path)
	if err == nil {
		_ = json.Unmarshal(b, &c.data)
	} else if !errors.Is(err, fs.ErrNotExist) {
		// Corrupt or unreadable — start fresh, don't block.
		c.data = make(map[string]Entry)
	}
	return c
}

func key(depotID uint32, manifestID uint64, filepath string) string {
	return fmt.Sprintf("%d_%d/%s", depotID, manifestID, filepath)
}

func (c *Cache) Get(depotID uint32, manifestID uint64, fp string) (Entry, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.data[key(depotID, manifestID, fp)]
	return e, ok
}

func (c *Cache) Set(depotID uint32, manifestID uint64, fp string, e Entry) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.data[key(depotID, manifestID, fp)] = e
	c.dirty = true
}

func (c *Cache) Flush() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.dirty {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(c.path), 0o755); err != nil {
		return err
	}
	b, err := json.Marshal(c.data)
	if err != nil {
		return err
	}
	if err := os.WriteFile(c.path, b, 0o644); err != nil {
		return err
	}
	c.dirty = false
	return nil
}
