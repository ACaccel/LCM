/*
Package config centralizes LCM_Enabler file-location configuration.

Values default to the current hard-coded relative paths, so with no environment
override behavior is identical.
*/
package config

import "os"

const (
	defaultUploadDir    = "efo/LCM_Enabler/upload"
	defaultTemplatesDir = "efo/LCM_Enabler/templates"
)

// UploadDir is the directory (relative to the EFO working directory) where
// uploaded service configs and manifest files are stored. Overridable via
// LCM_UPLOAD_DIR.
func UploadDir() string {
	if v := os.Getenv("LCM_UPLOAD_DIR"); v != "" {
		return v
	}
	return defaultUploadDir
}

// TemplatesDir is the directory (relative to the EFO working directory) where
// policy templates are stored. Overridable via LCM_TEMPLATES_DIR.
func TemplatesDir() string {
	if v := os.Getenv("LCM_TEMPLATES_DIR"); v != "" {
		return v
	}
	return defaultTemplatesDir
}
