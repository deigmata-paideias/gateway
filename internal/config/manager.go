package config

import (
	"errors"
	"fmt"
	"slices"
	"sync/atomic"
)

var (
	ErrRouteNotFound   = errors.New("config: route not found")
	ErrBackendNotFound = errors.New("config: backend not found")
)

type ResolvedRoute struct {
	Revision int64
	Route    Route
	Backend  Backend
	Target   Target
}

type Snapshot struct {
	revision int64
	config   Gateway
	backends map[string]Backend
	routes   map[string]Route
}

func NewSnapshot(revision int64, cfg Gateway) (*Snapshot, error) {
	if revision < 1 {
		return nil, fmt.Errorf("revision 必须为正数")
	}
	if err := ValidateGateway(cfg); err != nil {
		return nil, err
	}
	copyConfig := cloneGateway(cfg)
	snapshot := &Snapshot{
		revision: revision,
		config:   copyConfig,
		backends: make(map[string]Backend, len(copyConfig.Backends)),
		routes:   make(map[string]Route, len(copyConfig.Routes)),
	}
	for _, backend := range copyConfig.Backends {
		snapshot.backends[backend.ID] = backend
	}
	for _, route := range copyConfig.Routes {
		snapshot.routes[route.Operation+"\x00"+route.ModelAlias] = route
	}
	return snapshot, nil
}

func (s *Snapshot) Revision() int64 {
	return s.revision
}

func (s *Snapshot) Config() Gateway {
	return cloneGateway(s.config)
}

func (s *Snapshot) Resolve(operation, modelAlias string) (ResolvedRoute, error) {
	route, ok := s.routes[operation+"\x00"+modelAlias]
	if !ok {
		return ResolvedRoute{}, ErrRouteNotFound
	}
	backend, ok := s.backends[route.ActiveBackend]
	if !ok {
		return ResolvedRoute{}, ErrBackendNotFound
	}
	for _, target := range route.Targets {
		if target.BackendID == backend.ID {
			return ResolvedRoute{
				Revision: s.revision,
				Route:    cloneRoute(route),
				Backend:  cloneBackend(backend),
				Target:   target,
			}, nil
		}
	}
	return ResolvedRoute{}, ErrBackendNotFound
}

func (s *Snapshot) ResolveBackend(operation, modelAlias, backendID string) (ResolvedRoute, error) {
	route, ok := s.routes[operation+"\x00"+modelAlias]
	if !ok {
		return ResolvedRoute{}, ErrRouteNotFound
	}
	backend, ok := s.backends[backendID]
	if !ok {
		return ResolvedRoute{}, ErrBackendNotFound
	}
	for _, target := range route.Targets {
		if target.BackendID == backendID {
			return ResolvedRoute{
				Revision: s.revision,
				Route:    cloneRoute(route),
				Backend:  cloneBackend(backend),
				Target:   target,
			}, nil
		}
	}
	return ResolvedRoute{}, ErrBackendNotFound
}

func (s *Snapshot) Models() []Route {
	routes := make([]Route, 0, len(s.config.Routes))
	for _, route := range s.config.Routes {
		routes = append(routes, cloneRoute(route))
	}
	slices.SortFunc(routes, func(a, b Route) int {
		if a.ModelAlias < b.ModelAlias {
			return -1
		}
		if a.ModelAlias > b.ModelAlias {
			return 1
		}
		return 0
	})
	return routes
}

type Manager struct {
	current atomic.Pointer[Snapshot]
}

func NewManager(snapshot *Snapshot) *Manager {
	manager := &Manager{}
	manager.current.Store(snapshot)
	return manager
}

func (m *Manager) Current() *Snapshot {
	return m.current.Load()
}

func (m *Manager) Store(snapshot *Snapshot) {
	m.current.Store(snapshot)
}

func SwitchActive(cfg Gateway, routeID, backendID string) (Gateway, error) {
	updated := cloneGateway(cfg)
	for i := range updated.Routes {
		if updated.Routes[i].ID != routeID {
			continue
		}
		for _, target := range updated.Routes[i].Targets {
			if target.BackendID == backendID {
				updated.Routes[i].ActiveBackend = backendID
				return updated, ValidateGateway(updated)
			}
		}
		return Gateway{}, ErrBackendNotFound
	}
	return Gateway{}, ErrRouteNotFound
}

func cloneGateway(cfg Gateway) Gateway {
	cloned := cfg
	cloned.Backends = make([]Backend, len(cfg.Backends))
	for i, backend := range cfg.Backends {
		cloned.Backends[i] = cloneBackend(backend)
	}
	cloned.Routes = make([]Route, len(cfg.Routes))
	for i, route := range cfg.Routes {
		cloned.Routes[i] = cloneRoute(route)
	}
	return cloned
}

func cloneBackend(backend Backend) Backend {
	backend.Capabilities = slices.Clone(backend.Capabilities)
	return backend
}

func cloneRoute(route Route) Route {
	route.Targets = slices.Clone(route.Targets)
	return route
}
