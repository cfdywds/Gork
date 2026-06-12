package backends

import (
	"context"
	"os"
	"reflect"
	"strings"
	"testing"
)

func TestSQLConfigBackendLoadEnsuresTableAndUnflattensRows(t *testing.T) {
	engine := &fakeSQLEngine{rows: map[string]string{
		"model.name":      `"grok-2"`,
		"limits.requests": "8",
	}}
	backend := NewSQLConfigBackend(engine, SQLConfigOptions{Dialect: "mysql"})

	loaded, err := backend.Load(context.Background())
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}

	want := map[string]any{
		"model":  map[string]any{"name": "grok-2"},
		"limits": map[string]any{"requests": int64(8)},
	}
	if !reflect.DeepEqual(want, loaded) {
		t.Fatalf("loaded=%#v want=%#v", loaded, want)
	}
	if engine.ensureCount != 1 {
		t.Fatalf("ensureCount=%d want 1", engine.ensureCount)
	}
	if engine.ensuredTable.Name != "config_store" || engine.ensuredTable.KeyMaxLength != 255 ||
		engine.ensuredTable.KeyColumn != "key" || engine.ensuredTable.ValueColumn != "value" {
		t.Fatalf("ensured table = %#v", engine.ensuredTable)
	}
	if engine.loadTable != "config_store" || engine.loadExcludeKey != "__version__" {
		t.Fatalf("loadTable=%q exclude=%q", engine.loadTable, engine.loadExcludeKey)
	}

	if _, err := backend.Version(context.Background()); err != nil {
		t.Fatalf("Version returned error: %v", err)
	}
	if engine.ensureCount != 1 {
		t.Fatalf("ensure should be cached, got %d", engine.ensureCount)
	}
}

func TestSQLConfigBackendApplyPatchUpsertsFlattenedValuesAndIncrementsVersion(t *testing.T) {
	engine := &fakeSQLEngine{}
	backend := NewSQLConfigBackend(engine, SQLConfigOptions{Dialect: "postgresql"})

	err := backend.ApplyPatch(context.Background(), map[string]any{
		"model": map[string]any{"name": "grok-2"},
		"flags": map[string]any{"stream": true},
	})
	if err != nil {
		t.Fatalf("ApplyPatch returned error: %v", err)
	}

	if engine.ensureCount != 1 {
		t.Fatalf("ensureCount=%d want 1", engine.ensureCount)
	}
	if len(engine.transactions) != 1 {
		t.Fatalf("transactions=%d want 1", len(engine.transactions))
	}
	tx := engine.transactions[0]
	wantValues := map[string]string{
		"model.name":   `"grok-2"`,
		"flags.stream": "true",
	}
	if !reflect.DeepEqual(wantValues, tx.values) {
		t.Fatalf("values=%#v want=%#v", tx.values, wantValues)
	}
	if tx.dialect != "postgresql" || tx.table != "config_store" || tx.versionKey != "__version__" {
		t.Fatalf("tx state=%#v", tx)
	}
	if !tx.incremented || !tx.committed || tx.rolledBack {
		t.Fatalf("tx flags incremented=%t committed=%t rolledBack=%t", tx.incremented, tx.committed, tx.rolledBack)
	}
}

func TestSQLConfigBackendApplyPatchKeepsEmptyPatchOutOfTransaction(t *testing.T) {
	engine := &fakeSQLEngine{}
	backend := NewSQLConfigBackend(engine, SQLConfigOptions{})

	if err := backend.ApplyPatch(context.Background(), map[string]any{}); err != nil {
		t.Fatalf("empty ApplyPatch returned error: %v", err)
	}
	if engine.ensureCount != 1 {
		t.Fatalf("empty patch still ensures table, got %d", engine.ensureCount)
	}
	if len(engine.transactions) != 0 {
		t.Fatalf("empty patch should not start transaction")
	}
}

func TestSQLConfigBackendClearDeletesValuesAndIncrementsVersion(t *testing.T) {
	engine := &fakeSQLEngine{}
	backend := NewSQLConfigBackend(engine, SQLConfigOptions{Dialect: "mysql"})

	if err := backend.Clear(context.Background()); err != nil {
		t.Fatalf("Clear returned error: %v", err)
	}
	if engine.ensureCount != 1 {
		t.Fatalf("ensureCount=%d want 1", engine.ensureCount)
	}
	if len(engine.transactions) != 1 {
		t.Fatalf("transactions=%d want 1", len(engine.transactions))
	}
	tx := engine.transactions[0]
	if tx.dialect != "mysql" || tx.table != "config_store" || tx.deleteExcludeKey != "__version__" {
		t.Fatalf("tx state=%#v", tx)
	}
	if !tx.incremented || !tx.committed || tx.rolledBack {
		t.Fatalf("tx flags incremented=%t committed=%t rolledBack=%t", tx.incremented, tx.committed, tx.rolledBack)
	}
}

func TestSQLConfigBackendVersionAndClose(t *testing.T) {
	engine := &fakeSQLEngine{version: "42"}
	backend := NewSQLConfigBackend(engine, SQLConfigOptions{})

	version, err := backend.Version(context.Background())
	if err != nil {
		t.Fatalf("Version returned error: %v", err)
	}
	if version != int64(42) || engine.versionTable != "config_store" || engine.versionKey != "__version__" {
		t.Fatalf("version=%#v table=%q key=%q", version, engine.versionTable, engine.versionKey)
	}

	engine.version = ""
	version, err = backend.Version(context.Background())
	if err != nil {
		t.Fatalf("empty Version returned error: %v", err)
	}
	if version != int64(0) {
		t.Fatalf("empty version = %#v", version)
	}

	if err := backend.Close(context.Background()); err != nil {
		t.Fatalf("Close returned error: %v", err)
	}
	if !engine.disposed {
		t.Fatalf("Close should dispose by default")
	}

	engine = &fakeSQLEngine{}
	backend = NewSQLConfigBackend(engine, SQLConfigOptions{DisposeEngine: boolPtr(false)})
	if err := backend.Close(context.Background()); err != nil {
		t.Fatalf("Close without dispose returned error: %v", err)
	}
	if engine.disposed {
		t.Fatalf("DisposeEngine=false should skip dispose")
	}
}

func TestSQLConfigBackendContractDocumentsPythonVersionUpsertSemantics(t *testing.T) {
	content, err := os.ReadFile("sql.go")
	if err != nil {
		t.Fatalf("read sql.go: %v", err)
	}
	text := string(content)
	for _, want := range []string{
		"insert version row with value \"1\"",
		"atomically increment the integer text value",
		"PostgreSQL ON CONFLICT",
		"MySQL ON DUPLICATE KEY UPDATE",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("sql.go missing %q in:\n%s", want, text)
		}
	}
}

func boolPtr(value bool) *bool {
	return &value
}

type fakeSQLEngine struct {
	rows           map[string]string
	version        string
	ensureCount    int
	ensuredTable   SQLConfigTable
	loadTable      string
	loadExcludeKey string
	versionTable   string
	versionKey     string
	disposed       bool
	transactions   []*fakeSQLTx
}

func (e *fakeSQLEngine) EnsureConfigTable(_ context.Context, table SQLConfigTable) error {
	e.ensureCount++
	e.ensuredTable = table
	return nil
}

func (e *fakeSQLEngine) LoadConfigRows(_ context.Context, tableName, excludeKey string) (map[string]string, error) {
	e.loadTable = tableName
	e.loadExcludeKey = excludeKey
	return e.rows, nil
}

func (e *fakeSQLEngine) BeginConfigTransaction(context.Context) (SQLConfigTx, error) {
	tx := &fakeSQLTx{values: map[string]string{}}
	e.transactions = append(e.transactions, tx)
	return tx, nil
}

func (e *fakeSQLEngine) GetConfigValue(_ context.Context, tableName, key string) (string, error) {
	e.versionTable = tableName
	e.versionKey = key
	return e.version, nil
}

func (e *fakeSQLEngine) Dispose(context.Context) error {
	e.disposed = true
	return nil
}

type fakeSQLTx struct {
	dialect          string
	table            string
	versionKey       string
	deleteExcludeKey string
	values           map[string]string
	incremented      bool
	committed        bool
	rolledBack       bool
}

func (tx *fakeSQLTx) DeleteConfigValues(_ context.Context, dialect, tableName, excludeKey string) error {
	tx.dialect = dialect
	tx.table = tableName
	tx.deleteExcludeKey = excludeKey
	return nil
}

func (tx *fakeSQLTx) UpsertConfigValue(_ context.Context, dialect, tableName, key, value string) error {
	tx.dialect = dialect
	tx.table = tableName
	tx.values[key] = value
	return nil
}

func (tx *fakeSQLTx) IncrementConfigVersion(_ context.Context, dialect, tableName, versionKey string) error {
	tx.dialect = dialect
	tx.table = tableName
	tx.versionKey = versionKey
	tx.incremented = true
	return nil
}

func (tx *fakeSQLTx) Commit(context.Context) error {
	tx.committed = true
	return nil
}

func (tx *fakeSQLTx) Rollback(context.Context) error {
	tx.rolledBack = true
	return nil
}
