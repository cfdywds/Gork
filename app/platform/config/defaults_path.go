package config

import (
	"os"
	"path/filepath"
)

func resolveDefaultsPathFromLocations(callerFile, workDir, fileName string) string {
	if fileName == "" {
		fileName = "config.defaults.toml"
	}
	if path := defaultsPathFromAbsoluteCaller(callerFile, fileName); path != "" {
		return path
	}
	if path := findDefaultsPathUpward(workDir, fileName); path != "" {
		return path
	}
	if filepath.IsAbs(fileName) {
		return filepath.Clean(fileName)
	}
	if workDir != "" {
		return filepath.Clean(filepath.Join(workDir, fileName))
	}
	return fileName
}

func defaultsPathFromAbsoluteCaller(callerFile, fileName string) string {
	if callerFile == "" || !filepath.IsAbs(callerFile) {
		return ""
	}
	path := filepath.Clean(filepath.Join(filepath.Dir(callerFile), "..", "..", "..", fileName))
	if configFileExists(path) {
		return path
	}
	return ""
}

func findDefaultsPathUpward(startDir, fileName string) string {
	if startDir == "" {
		return ""
	}
	dir := filepath.Clean(startDir)
	for {
		path := filepath.Join(dir, fileName)
		if configFileExists(path) {
			return path
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}

func configFileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}
