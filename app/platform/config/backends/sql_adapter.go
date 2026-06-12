package backends

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	_ "github.com/go-sql-driver/mysql"
	_ "github.com/jackc/pgx/v5/stdlib"
)

const (
	configSQLMaxIdleConns    = 5
	configSQLMaxOpenConns    = 15
	configSQLConnMaxLifetime = 30 * time.Minute
)

type databaseSQLConfigEngine struct {
	db      *sql.DB
	dialect string
}

type databaseSQLConfigTx struct {
	tx *sql.Tx
}

func newDatabaseSQLConfigBackend(dialect, rawURL string) (ConfigBackend, error) {
	connectString, err := prepareConfigSQLConnectString(dialect, rawURL)
	if err != nil {
		return nil, err
	}
	driver, err := configSQLDriverName(dialect)
	if err != nil {
		return nil, err
	}
	db, err := sql.Open(driver, connectString)
	if err != nil {
		return nil, err
	}
	configureConfigSQLPool(db)
	engine := &databaseSQLConfigEngine{db: db, dialect: dialect}
	return NewSQLConfigBackend(engine, SQLConfigOptions{Dialect: dialect}), nil
}

func (e *databaseSQLConfigEngine) EnsureConfigTable(ctx context.Context, table SQLConfigTable) error {
	tableName, keyColumn, valueColumn, err := e.tableIdentifiers(table)
	if err != nil {
		return err
	}
	valueType := "TEXT"
	if e.dialect == "mysql" {
		valueType = "LONGTEXT"
	}
	statement := fmt.Sprintf(
		"CREATE TABLE IF NOT EXISTS %s (%s VARCHAR(%d) PRIMARY KEY, %s %s NOT NULL)",
		tableName, keyColumn, table.KeyMaxLength, valueColumn, valueType,
	)
	_, err = e.db.ExecContext(ctx, statement)
	return err
}

func (e *databaseSQLConfigEngine) LoadConfigRows(
	ctx context.Context,
	tableName string,
	excludeKey string,
) (map[string]string, error) {
	table, keyColumn, valueColumn, err := configSQLStoreIdentifiers(e.dialect, tableName)
	if err != nil {
		return nil, err
	}
	statement := fmt.Sprintf("SELECT %s, %s FROM %s WHERE %s <> %s",
		keyColumn, valueColumn, table, keyColumn, e.placeholder(1))
	rows, err := e.db.QueryContext(ctx, statement, excludeKey)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanConfigRows(rows)
}

func (e *databaseSQLConfigEngine) BeginConfigTransaction(ctx context.Context) (SQLConfigTx, error) {
	tx, err := e.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	return &databaseSQLConfigTx{tx: tx}, nil
}

func (e *databaseSQLConfigEngine) GetConfigValue(ctx context.Context, tableName, key string) (string, error) {
	table, keyColumn, valueColumn, err := configSQLStoreIdentifiers(e.dialect, tableName)
	if err != nil {
		return "", err
	}
	statement := fmt.Sprintf("SELECT %s FROM %s WHERE %s = %s",
		valueColumn, table, keyColumn, e.placeholder(1))
	var value string
	err = e.db.QueryRowContext(ctx, statement, key).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return value, err
}

func (e *databaseSQLConfigEngine) Dispose(context.Context) error {
	return e.db.Close()
}

func (e *databaseSQLConfigEngine) tableIdentifiers(table SQLConfigTable) (string, string, string, error) {
	if table.KeyMaxLength <= 0 {
		table.KeyMaxLength = 255
	}
	return configSQLIdentifiers(e.dialect, table.Name, table.KeyColumn, table.ValueColumn)
}

func (e *databaseSQLConfigEngine) placeholder(position int) string {
	if e.dialect == "postgresql" {
		return fmt.Sprintf("$%d", position)
	}
	return "?"
}

func (tx *databaseSQLConfigTx) DeleteConfigValues(ctx context.Context, dialect, tableName, excludeKey string) error {
	statement, err := configSQLDeleteValuesStatement(dialect, tableName)
	if err != nil {
		return err
	}
	_, err = tx.tx.ExecContext(ctx, statement, excludeKey)
	return err
}

func (tx *databaseSQLConfigTx) UpsertConfigValue(ctx context.Context, dialect, tableName, key, value string) error {
	statement, err := configSQLUpsertStatement(dialect, tableName)
	if err != nil {
		return err
	}
	_, err = tx.tx.ExecContext(ctx, statement, key, value)
	return err
}

func (tx *databaseSQLConfigTx) IncrementConfigVersion(
	ctx context.Context,
	dialect string,
	tableName string,
	versionKey string,
) error {
	statement, err := configSQLIncrementVersionStatement(dialect, tableName)
	if err != nil {
		return err
	}
	_, err = tx.tx.ExecContext(ctx, statement, versionKey)
	return err
}

func (tx *databaseSQLConfigTx) Commit(context.Context) error {
	return tx.tx.Commit()
}

func (tx *databaseSQLConfigTx) Rollback(context.Context) error {
	return tx.tx.Rollback()
}

func prepareConfigSQLConnectString(dialect, rawURL string) (string, error) {
	switch dialect {
	case "mysql":
		return prepareConfigMySQLDSN(rawURL)
	case "postgresql":
		return normalizeConfigPostgreSQLURL(rawURL), nil
	default:
		return "", fmt.Errorf("unsupported SQL config backend dialect: %s", dialect)
	}
}

func prepareConfigMySQLDSN(rawURL string) (string, error) {
	raw := strings.TrimSpace(rawURL)
	if raw == "" {
		return "", errors.New("mysql config backend URL is required")
	}
	if !strings.Contains(raw, "://") {
		return raw, nil
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return "", err
	}
	if !isConfigMySQLScheme(parsed.Scheme) {
		return raw, nil
	}
	query := ""
	if parsed.RawQuery != "" {
		query = "?" + parsed.RawQuery
	}
	return fmt.Sprintf("%s@tcp(%s)/%s%s",
		parsed.User.String(), parsed.Host, strings.TrimPrefix(parsed.EscapedPath(), "/"), query), nil
}

func normalizeConfigPostgreSQLURL(rawURL string) string {
	raw := strings.TrimSpace(rawURL)
	replacements := []struct{ from, to string }{
		{"postgresql+asyncpg://", "postgres://"},
		{"postgres+asyncpg://", "postgres://"},
		{"postgresql://", "postgres://"},
		{"pgsql://", "postgres://"},
	}
	for _, item := range replacements {
		if strings.HasPrefix(raw, item.from) {
			return item.to + raw[len(item.from):]
		}
	}
	return raw
}

func configSQLDriverName(dialect string) (string, error) {
	switch dialect {
	case "mysql":
		return "mysql", nil
	case "postgresql":
		return "pgx", nil
	default:
		return "", fmt.Errorf("unsupported SQL config backend dialect: %s", dialect)
	}
}

func configureConfigSQLPool(db *sql.DB) {
	db.SetMaxIdleConns(configSQLMaxIdleConns)
	db.SetMaxOpenConns(configSQLMaxOpenConns)
	db.SetConnMaxLifetime(configSQLConnMaxLifetime)
}

func scanConfigRows(rows *sql.Rows) (map[string]string, error) {
	out := map[string]string{}
	for rows.Next() {
		var key, value string
		if err := rows.Scan(&key, &value); err != nil {
			return nil, err
		}
		out[key] = value
	}
	return out, rows.Err()
}

func isConfigMySQLScheme(scheme string) bool {
	switch scheme {
	case "mysql", "mariadb", "mysql+aiomysql", "mariadb+aiomysql":
		return true
	default:
		return false
	}
}
