package software

import (
	"fmt"
	"sync"

	"github.com/szilab/RunPilot/internal/model"
)

// Manager caches providers by their effective root. Switching configuration
// creates a new provider and intentionally leaves the previous root untouched.
type Manager struct {
	dataDir string
	mu      sync.Mutex
	items   map[string]cachedProvider
}

type cachedProvider struct {
	root string
	p    Provider
}

func NewManager(dataDir string) *Manager {
	return &Manager{dataDir: dataDir, items: map[string]cachedProvider{}}
}

func (m *Manager) Provider(d model.SoftwareProviderDefinition) (Provider, error) {
	validated, err := ValidateDefinition(m.dataDir, d)
	if err != nil {
		return nil, err
	}
	root, _, err := EffectiveRoot(m.dataDir, validated)
	if err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if existing, ok := m.items[validated.ID]; ok && existing.root == root {
		return existing.p, nil
	}
	p, err := NewScoop(validated, m.dataDir, ScoopOptions{})
	if err != nil {
		return nil, err
	}
	m.items[validated.ID] = cachedProvider{root: root, p: p}
	return p, nil
}

func Find(definitions []model.SoftwareProviderDefinition, id string) (model.SoftwareProviderDefinition, error) {
	for _, d := range definitions {
		if d.ID == id {
			return d, nil
		}
	}
	return model.SoftwareProviderDefinition{}, fmt.Errorf("unknown software provider %q", id)
}
