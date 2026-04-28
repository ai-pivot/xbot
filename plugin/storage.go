package plugin

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
)

// ---------------------------------------------------------------------------
// File-based per-plugin KV storage
// ---------------------------------------------------------------------------

// fileStorage implements StorageAccessor using a JSON file per plugin.
// File location: ~/.xbot/plugins/<id>/storage.json
type fileStorage struct {
	mu   sync.RWMutex
	path string
	data map[string]string
}

// NewFileStorage creates or loads a file-based storage for the given plugin.
func NewFileStorage(pluginDir string) (StorageAccessor, error) {
	storageDir := filepath.Join(pluginDir, "data")
	if err := os.MkdirAll(storageDir, 0755); err != nil {
		return nil, err
	}

	fs := &fileStorage{
		path: filepath.Join(storageDir, "storage.json"),
		data: make(map[string]string),
	}

	// Load existing data
	if data, err := os.ReadFile(fs.path); err == nil {
		_ = json.Unmarshal(data, &fs.data)
	}

	return fs, nil
}

func (fs *fileStorage) Get(key string) (string, bool) {
	fs.mu.RLock()
	defer fs.mu.RUnlock()
	v, ok := fs.data[key]
	return v, ok
}

func (fs *fileStorage) Set(key, value string) error {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	fs.data[key] = value
	return fs.persist()
}

func (fs *fileStorage) Delete(key string) error {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	delete(fs.data, key)
	return fs.persist()
}

func (fs *fileStorage) Keys() []string {
	fs.mu.RLock()
	defer fs.mu.RUnlock()
	keys := make([]string, 0, len(fs.data))
	for k := range fs.data {
		keys = append(keys, k)
	}
	return keys
}

func (fs *fileStorage) Clear() error {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	fs.data = make(map[string]string)
	return fs.persist()
}

func (fs *fileStorage) persist() error {
	data, err := json.MarshalIndent(fs.data, "", "  ")
	if err != nil {
		return err
	}
	tmp := fs.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0600); err != nil {
		return err
	}
	return os.Rename(tmp, fs.path)
}
