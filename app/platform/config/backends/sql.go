package backends

import (
	"context"
	"strconv"
)

const (
	defaultSQLConfigTableName  = "config_store"
	defaultSQLConfigVersionKey = "__version__"
	defaultSQLConfigDialect    = "postgresql"
)

type SQLConfigTable struct {
	Name         string
	KeyColumn    string
	KeyMaxLength int
	ValueColumn  string
}

type SQLConfigEngine interface {
	EnsureConfigTable(ctx context.Context, table SQLConfigTable) error
	LoadConfigRows(ctx context.Context, tableName, excludeKey string) (map[string]string, error)
	BeginConfigTransaction(ctx context.Context) (SQLConfigTx, error)
	GetConfigValue(ctx context.Context, tableName, key string) (string, error)
	Dispose(ctx context.Context) error
}

type SQLConfigTx interface {
	DeleteConfigValues(ctx context.Context, dialect, tableName, excludeKey string) error
	UpsertConfigValue(ctx context.Context, dialect, tableName, key, value string) error
	// IncrementConfigVersion mirrors the Python version UPSERT.
	// It must insert version row with value "1" when missing.
	// It must atomically increment the integer text value when present.
	// Implementations should use
	// PostgreSQL ON CONFLICT or MySQL ON DUPLICATE KEY UPDATE for dialect-
	// specific SQL.
	IncrementConfigVersion(ctx context.Context, dialect, tableName, versionKey string) error
	Commit(ctx context.Context) error
	Rollback(ctx context.Context) error
}

type SQLConfigOptions struct {
	Dialect       string
	DisposeEngine *bool
}

type SQLConfigBackend struct {
	engine        SQLConfigEngine
	dialect       string
	disposeEngine bool
	ready         bool
}

func NewSQLConfigBackend(engine SQLConfigEngine, options SQLConfigOptions) *SQLConfigBackend {
	dialect := options.Dialect
	if dialect == "" {
		dialect = defaultSQLConfigDialect
	}
	disposeEngine := true
	if options.DisposeEngine != nil {
		disposeEngine = *options.DisposeEngine
	}
	return &SQLConfigBackend{
		engine:        engine,
		dialect:       dialect,
		disposeEngine: disposeEngine,
	}
}

func (b *SQLConfigBackend) Load(ctx context.Context) (map[string]any, error) {
	if err := b.ensureTable(ctx); err != nil {
		return nil, err
	}
	flat, err := b.engine.LoadConfigRows(ctx, defaultSQLConfigTableName, defaultSQLConfigVersionKey)
	if err != nil {
		return nil, err
	}
	return Unflatten(flat), nil
}

func (b *SQLConfigBackend) ApplyPatch(ctx context.Context, patch map[string]any) error {
	if err := b.ensureTable(ctx); err != nil {
		return err
	}
	flat, err := Flatten(patch)
	if err != nil {
		return err
	}
	if len(flat) == 0 {
		return nil
	}
	tx, err := b.engine.BeginConfigTransaction(ctx)
	if err != nil {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(ctx)
		}
	}()
	for key, value := range flat {
		if err := tx.UpsertConfigValue(ctx, b.dialect, defaultSQLConfigTableName, key, value); err != nil {
			return err
		}
	}
	if err := tx.IncrementConfigVersion(ctx, b.dialect, defaultSQLConfigTableName, defaultSQLConfigVersionKey); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	committed = true
	return nil
}

func (b *SQLConfigBackend) Clear(ctx context.Context) error {
	if err := b.ensureTable(ctx); err != nil {
		return err
	}
	tx, err := b.engine.BeginConfigTransaction(ctx)
	if err != nil {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(ctx)
		}
	}()
	if err := tx.DeleteConfigValues(ctx, b.dialect, defaultSQLConfigTableName, defaultSQLConfigVersionKey); err != nil {
		return err
	}
	if err := tx.IncrementConfigVersion(ctx, b.dialect, defaultSQLConfigTableName, defaultSQLConfigVersionKey); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	committed = true
	return nil
}

func (b *SQLConfigBackend) Version(ctx context.Context) (any, error) {
	if err := b.ensureTable(ctx); err != nil {
		return nil, err
	}
	raw, err := b.engine.GetConfigValue(ctx, defaultSQLConfigTableName, defaultSQLConfigVersionKey)
	if err != nil {
		return nil, err
	}
	if raw == "" {
		return int64(0), nil
	}
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return nil, err
	}
	return value, nil
}

func (b *SQLConfigBackend) Close(ctx context.Context) error {
	if !b.disposeEngine {
		return nil
	}
	return b.engine.Dispose(ctx)
}

func (b *SQLConfigBackend) ensureTable(ctx context.Context) error {
	if b.ready {
		return nil
	}
	if err := b.engine.EnsureConfigTable(ctx, SQLConfigTable{
		Name:         defaultSQLConfigTableName,
		KeyColumn:    "key",
		KeyMaxLength: 255,
		ValueColumn:  "value",
	}); err != nil {
		return err
	}
	b.ready = true
	return nil
}
