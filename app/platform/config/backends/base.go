package backends

import "context"

// ConfigBackend persists and reloads user config overrides.
//
// Storage uses flat key-value pairs: dotted keys map to JSON-serialized values.
// Load rebuilds the full nested map from all stored pairs, while ApplyPatch
// persists only the keys present in patch and leaves the rest untouched.
// Version returns an opaque token that is cheap to call on every request.
// Close mirrors the optional Python close hook; implementations that hold no
// resources should return nil.
type ConfigBackend interface {
	// Load returns the full stored user-overrides as a nested map.
	Load(context.Context) (map[string]any, error)

	// ApplyPatch persists only the keys present in patch.
	ApplyPatch(context.Context, map[string]any) error

	// Clear removes all stored user-overrides.
	Clear(context.Context) error

	// Version returns an opaque version token.
	Version(context.Context) (any, error)

	// Close releases backend resources.
	Close(context.Context) error
}
