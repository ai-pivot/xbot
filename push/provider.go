package push

import (
	"context"
	"sort"

	"xbot/storage"
)

// Provider is one delivery transport (FCM / APNs / WebPush / Feishu / ...).
//
// Validate runs at registration time; Send runs per event. Implementations
// must classify their errors with PermanentError / TransientError / AuthError.
type Provider interface {
	Name() string
	Platform() string
	Validate(t storage.PushTarget) error
	Send(ctx context.Context, t storage.PushTarget, ev Event) error
}

// Registry holds the enabled providers keyed by Name().
type Registry struct {
	providers map[string]Provider
}

// NewRegistry builds a registry; nil providers are ignored.
func NewRegistry(providers ...Provider) *Registry {
	m := make(map[string]Provider, len(providers))
	for _, p := range providers {
		if p != nil {
			m[p.Name()] = p
		}
	}
	return &Registry{providers: m}
}

// Get returns the provider by name.
func (r *Registry) Get(name string) (Provider, bool) {
	if r == nil {
		return nil, false
	}
	p, ok := r.providers[name]
	return p, ok
}

// Names returns the enabled provider names, sorted.
func (r *Registry) Names() []string {
	if r == nil {
		return nil
	}
	out := make([]string, 0, len(r.providers))
	for name := range r.providers {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// Empty reports whether no provider is enabled.
func (r *Registry) Empty() bool { return r == nil || len(r.providers) == 0 }
