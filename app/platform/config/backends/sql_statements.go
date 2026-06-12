package backends

import (
	"fmt"
	"unicode"
)

func configSQLStoreIdentifiers(dialect, tableName string) (string, string, string, error) {
	return configSQLIdentifiers(dialect, tableName, "key", "value")
}

func configSQLIdentifiers(dialect, tableName, keyColumn, valueColumn string) (string, string, string, error) {
	table, err := quoteConfigSQLIdentifier(dialect, tableName)
	if err != nil {
		return "", "", "", err
	}
	key, err := quoteConfigSQLIdentifier(dialect, keyColumn)
	if err != nil {
		return "", "", "", err
	}
	value, err := quoteConfigSQLIdentifier(dialect, valueColumn)
	if err != nil {
		return "", "", "", err
	}
	return table, key, value, nil
}

func quoteConfigSQLIdentifier(dialect, name string) (string, error) {
	if !validConfigSQLIdentifier(name) {
		return "", fmt.Errorf("invalid SQL config identifier: %q", name)
	}
	if dialect == "mysql" {
		return "`" + name + "`", nil
	}
	return `"` + name + `"`, nil
}

func validConfigSQLIdentifier(name string) bool {
	if name == "" {
		return false
	}
	for _, r := range name {
		if r != '_' && !unicode.IsLetter(r) && !unicode.IsDigit(r) {
			return false
		}
	}
	return true
}

func configSQLUpsertStatement(dialect, tableName string) (string, error) {
	table, keyColumn, valueColumn, err := configSQLStoreIdentifiers(dialect, tableName)
	if err != nil {
		return "", err
	}
	if dialect == "mysql" {
		return fmt.Sprintf(
			"INSERT INTO %s (%s, %s) VALUES (?, ?) ON DUPLICATE KEY UPDATE %s = VALUES(%s)",
			table, keyColumn, valueColumn, valueColumn, valueColumn,
		), nil
	}
	return fmt.Sprintf(
		"INSERT INTO %s (%s, %s) VALUES ($1, $2) ON CONFLICT (%s) DO UPDATE SET %s = EXCLUDED.%s",
		table, keyColumn, valueColumn, keyColumn, valueColumn, valueColumn,
	), nil
}

func configSQLDeleteValuesStatement(dialect, tableName string) (string, error) {
	table, keyColumn, _, err := configSQLStoreIdentifiers(dialect, tableName)
	if err != nil {
		return "", err
	}
	placeholder := "?"
	if dialect == "postgresql" {
		placeholder = "$1"
	}
	return fmt.Sprintf("DELETE FROM %s WHERE %s <> %s", table, keyColumn, placeholder), nil
}

func configSQLIncrementVersionStatement(dialect, tableName string) (string, error) {
	table, keyColumn, valueColumn, err := configSQLStoreIdentifiers(dialect, tableName)
	if err != nil {
		return "", err
	}
	if dialect == "mysql" {
		return fmt.Sprintf(
			"INSERT INTO %s (%s, %s) VALUES (?, '1') ON DUPLICATE KEY UPDATE %s = CAST(CAST(%s AS SIGNED) + 1 AS CHAR)",
			table, keyColumn, valueColumn, valueColumn, valueColumn,
		), nil
	}
	return fmt.Sprintf(
		"INSERT INTO %s (%s, %s) VALUES ($1, '1') ON CONFLICT (%s) DO UPDATE SET %s = (CAST(%s.%s AS BIGINT) + 1)::text",
		table, keyColumn, valueColumn, keyColumn, valueColumn, table, valueColumn,
	), nil
}
