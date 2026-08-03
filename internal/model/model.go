package model

import "time"

type Head struct {
	ID      string `json:"id"`
	EnvFile string `json:"env_file"`
}

type Registry struct {
	Heads map[string]Head `json:"heads"`
}

type Backup struct {
	Filename   string `json:"filename"`
	SHA256     string `json:"sha256"`
	DataBase64 string `json:"data_base64"`
	RestoreURL string `json:"restore_url,omitempty"`
}

type UpdateRequest struct {
	RequestID string `json:"request_id"`
	HeadID    string `json:"head_id"`
	Service   string `json:"service"`
	Version   string `json:"version,omitempty"`
	Backup    Backup `json:"backup"`
}

type Job struct {
	ID                string     `json:"id"`
	RequestID         string     `json:"request_id"`
	HeadID            string     `json:"head_id"`
	Service           string     `json:"service"`
	Version           string     `json:"version,omitempty"`
	State             string     `json:"state"`
	Message           string     `json:"message,omitempty"`
	BackupPath        string     `json:"backup_path,omitempty"`
	PreviousImage     string     `json:"previous_image,omitempty"`
	PreviousVersion   string     `json:"previous_version,omitempty"`
	InstalledImage    string     `json:"installed_image,omitempty"`
	InstalledVersion  string     `json:"installed_version,omitempty"`
	RollbackAvailable bool       `json:"rollback_available"`
	CreatedAt         time.Time  `json:"created_at"`
	UpdatedAt         time.Time  `json:"updated_at"`
	FinishedAt        *time.Time `json:"finished_at,omitempty"`
}

type ReleaseManifest struct {
	SchemaVersion int    `json:"schema_version"`
	Service       string `json:"service"`
	Version       string `json:"version"`
	Image         struct {
		Reference string `json:"reference"`
		Digest    string `json:"digest"`
	} `json:"image"`
	ComposeBundle struct {
		URL    string `json:"url"`
		SHA256 string `json:"sha256"`
	} `json:"compose_bundle"`
	DatabaseSchema        int    `json:"database_schema"`
	MinimumUpdaterVersion string `json:"minimum_updater_version"`
}
