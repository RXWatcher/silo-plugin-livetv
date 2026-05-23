// Package runtime implements the live TV plugin's Runtime gRPC server.
//
// Phase 1 is intentionally minimal: GetManifest returns the embedded
// manifest and Configure is a no-op. The plugin reads its DSN from the
// PLUGIN_CONFIG_DATABASE_URL env var at startup rather than from the
// host's Configure RPC. Later phases will move config to the host-managed
// path mirroring silo-plugin-audiobooks.
package runtime

import (
	"context"

	pluginv1 "github.com/ContinuumApp/continuum-plugin-sdk/pkg/pluginproto/silo/plugin/v1"
	"github.com/ContinuumApp/continuum-plugin-sdk/pkg/pluginsdk/runtimedefault"
)

// Server implements the plugin's Runtime service.
type Server struct {
	runtimedefault.Server
	manifest *pluginv1.PluginManifest
}

// New constructs a runtime server bound to the given manifest.
func New(manifest *pluginv1.PluginManifest) *Server {
	return &Server{manifest: manifest}
}

// GetManifest returns the embedded plugin manifest.
func (s *Server) GetManifest(_ context.Context, _ *pluginv1.GetManifestRequest) (*pluginv1.GetManifestResponse, error) {
	return &pluginv1.GetManifestResponse{Manifest: s.manifest}, nil
}

// Configure is a no-op in Phase 1. The host will still invoke it, but the
// plugin reads its DSN from the environment at startup so the call is
// accepted unconditionally.
func (s *Server) Configure(_ context.Context, _ *pluginv1.ConfigureRequest) (*pluginv1.ConfigureResponse, error) {
	return &pluginv1.ConfigureResponse{}, nil
}
