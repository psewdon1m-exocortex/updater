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
	SpoolID    string `json:"spool_id,omitempty"`
}

type UpdateRequest struct {
	RequestID     string `json:"request_id"`
	HeadID        string `json:"head_id"`
	Service       string `json:"service"`
	Version       string `json:"version,omitempty"`
	Backup        Backup `json:"backup"`
	BackupReceipt string `json:"backup_receipt,omitempty"`
	OperatorSaved bool   `json:"operator_saved,omitempty"`
	PreparationID string `json:"preparation_id,omitempty"`
}

type NeptuneInitializationRequest struct {
	RequestID      string `json:"request_id"`
	HeadID         string `json:"head_id"`
	ProjectID      string `json:"project_id"`
	ExportURL      string `json:"export_url"`
	EnrollmentCode string `json:"enrollment_code"`
}

type Job struct {
	ID                     string            `json:"id"`
	RequestID              string            `json:"request_id"`
	HeadID                 string            `json:"head_id"`
	Service                string            `json:"service"`
	Version                string            `json:"version,omitempty"`
	State                  string            `json:"state"`
	Message                string            `json:"message,omitempty"`
	BackupPath             string            `json:"backup_path,omitempty"`
	RecoveryMode           string            `json:"recovery_mode,omitempty"`
	BackupSHA256           string            `json:"backup_sha256,omitempty"`
	BackupFilename         string            `json:"backup_filename,omitempty"`
	Progress               Progress          `json:"progress"`
	PreviousImage          string            `json:"previous_image,omitempty"`
	PreviousWebImage       string            `json:"previous_web_image,omitempty"`
	DeploymentSnapshot     string            `json:"deployment_snapshot,omitempty"`
	MutationStarted        bool              `json:"mutation_started,omitempty"`
	PreviousVersion        string            `json:"previous_version,omitempty"`
	InstalledImage         string            `json:"installed_image,omitempty"`
	InstalledVersion       string            `json:"installed_version,omitempty"`
	RecoveryImage          string            `json:"recovery_image,omitempty"`
	RollbackAvailable      bool              `json:"rollback_available"`
	CreatedAt              time.Time         `json:"created_at"`
	UpdatedAt              time.Time         `json:"updated_at"`
	FinishedAt             *time.Time        `json:"finished_at,omitempty"`
	BackupSpoolID          string            `json:"backup_spool_id,omitempty"`
	RecoveryPending        bool              `json:"recovery_pending,omitempty"`
	PreparationID          string            `json:"preparation_id,omitempty"`
	RollbackOf             string            `json:"rollback_of,omitempty"`
	ManifestSHA256         string            `json:"manifest_sha256,omitempty"`
	PreviousManifestSHA256 string            `json:"previous_manifest_sha256,omitempty"`
	ComponentImages        map[string]string `json:"component_images,omitempty"`
	PreviousComponents     map[string]string `json:"previous_components,omitempty"`
	PreviousSchema         int               `json:"previous_schema,omitempty"`
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
	DatabaseSchema        int                `json:"database_schema"`
	MinimumUpdaterVersion string             `json:"minimum_updater_version"`
	WebImage              string             `json:"web_image,omitempty"`
	RollbackRestore       string             `json:"rollback_restore,omitempty"`
	Mastermind            *MastermindRelease `json:"mastermind,omitempty"`
}

type MastermindRelease struct {
	Profile             string            `json:"profile"`
	SourceSHA           string            `json:"source_sha"`
	Platform            string            `json:"platform"`
	Components          map[string]string `json:"components"`
	BridgeVersion       string            `json:"bridge_version"`
	ObsidianVersion     string            `json:"obsidian_version"`
	ModelSHA256         string            `json:"model_sha256"`
	MinimumSourceSchema int               `json:"minimum_source_schema"`
	MaximumSourceSchema int               `json:"maximum_source_schema"`
	SavedCopyProtocol   int               `json:"saved_copy_protocol"`
	Dependencies        map[string]string `json:"dependencies"`
	HealthProfile       string            `json:"health_profile"`
}
