// Package providers - Dynamic provider registry
package providers

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"sync"
)

// Registry manages all available providers with hot-reload support
type Registry struct {
	db        *sql.DB
	providers map[string]Provider
	current   string
	mu        sync.RWMutex
}

// NewRegistry creates a new provider registry
func NewRegistry(db *sql.DB) *Registry {
	r := &Registry{
		db:        db,
		providers: make(map[string]Provider),
	}
	r.reload()
	return r
}

// reload loads providers from database
func (r *Registry) reload() error {
	r.mu.Lock()
	defer r.mu.Unlock()

	rows, err := r.db.Query(`
		SELECT provider_id, name, base_url, api_key_env, default_model, enabled, priority, rate_limit_rpm, config
		FROM providers WHERE enabled = 1 ORDER BY priority
	`)
	if err != nil {
		return err
	}
	defer rows.Close()

	for rows.Next() {
		var cfg ProviderConfig
		var configJSON string
		var rateLimit sql.NullInt64

		err := rows.Scan(&cfg.ID, &cfg.Name, &cfg.BaseURL, &cfg.APIKeyEnv, &cfg.DefaultModel,
			&cfg.Enabled, &cfg.Priority, &rateLimit, &configJSON)
		if err != nil {
			continue
		}

		if rateLimit.Valid {
			cfg.RateLimitRPM = int(rateLimit.Int64)
		}

		// Create provider based on ID
		switch cfg.ID {
		case "cerebras":
			r.providers[cfg.ID] = NewCerebrasProvider(&cfg)
		case "openrouter":
			r.providers[cfg.ID] = NewOpenRouterProvider(&cfg)
		default:
			// Try to create a generic OpenAI-compatible provider
			r.providers[cfg.ID] = NewGenericProvider(&cfg)
		}
	}

	// Set current to first available provider
	if r.current == "" {
		for id, p := range r.providers {
			if p.IsAvailable() {
				r.current = id
				break
			}
		}
	}

	return nil
}

// Get returns a provider by ID
func (r *Registry) Get(id string) (Provider, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	p, ok := r.providers[id]
	if !ok {
		return nil, fmt.Errorf("provider %q not found", id)
	}
	return p, nil
}

// Current returns the current active provider
func (r *Registry) Current() Provider {
	r.mu.RLock()
	defer r.mu.RUnlock()

	if r.current == "" {
		return nil
	}
	return r.providers[r.current]
}

// SetCurrent sets the current provider
func (r *Registry) SetCurrent(id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if _, ok := r.providers[id]; !ok {
		return fmt.Errorf("provider %q not found", id)
	}

	r.current = id
	return nil
}

// List returns all available providers
func (r *Registry) List() []Provider {
	r.mu.RLock()
	defer r.mu.RUnlock()

	list := make([]Provider, 0, len(r.providers))
	for _, p := range r.providers {
		list = append(list, p)
	}
	return list
}

// Available returns providers that are configured and available
func (r *Registry) Available() []Provider {
	r.mu.RLock()
	defer r.mu.RUnlock()

	list := make([]Provider, 0)
	for _, p := range r.providers {
		if p.IsAvailable() {
			list = append(list, p)
		}
	}
	return list
}

// Reload reloads providers from database
func (r *Registry) Reload() error {
	return r.reload()
}

// Register adds a new provider to the database
func (r *Registry) Register(cfg *ProviderConfig) error {
	configJSON, _ := json.Marshal(map[string]interface{}{})

	_, err := r.db.Exec(`
		INSERT INTO providers (provider_id, name, base_url, api_key_env, default_model, enabled, priority, rate_limit_rpm, config)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(provider_id) DO UPDATE SET
			name = excluded.name,
			base_url = excluded.base_url,
			api_key_env = excluded.api_key_env,
			default_model = excluded.default_model,
			enabled = excluded.enabled,
			priority = excluded.priority,
			rate_limit_rpm = excluded.rate_limit_rpm,
			config = excluded.config
	`, cfg.ID, cfg.Name, cfg.BaseURL, cfg.APIKeyEnv, cfg.DefaultModel, cfg.Enabled, cfg.Priority, cfg.RateLimitRPM, string(configJSON))

	if err != nil {
		return err
	}

	return r.reload()
}

// GenericProvider is a generic OpenAI-compatible provider
type GenericProvider struct {
	config *ProviderConfig
	*CerebrasProvider // Embed Cerebras for OpenAI-compatible behavior
}

// NewGenericProvider creates a generic OpenAI-compatible provider
func NewGenericProvider(config *ProviderConfig) *GenericProvider {
	return &GenericProvider{
		config:           config,
		CerebrasProvider: NewCerebrasProvider(config),
	}
}

// ID returns the provider identifier
func (p *GenericProvider) ID() string {
	return p.config.ID
}

// Name returns the human-readable name
func (p *GenericProvider) Name() string {
	return p.config.Name
}
