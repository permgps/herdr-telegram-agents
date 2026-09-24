package domain

import "time"

// UpdateJob is the durable installation journal. A terminal job stays on
// disk until the next update so crash recovery and manual repair have facts.
type UpdateJob struct {
	ID                 string    `json:"id"`
	Phase              string    `json:"phase"`
	OldVersion         string    `json:"old_version"`
	OldBinaryVersion   string    `json:"old_binary_version"`
	TargetVersion      string    `json:"target_version"`
	TargetTag          string    `json:"target_tag"`
	TargetCommit       string    `json:"target_commit,omitempty"`
	TargetChecksum     string    `json:"target_checksum"`
	TargetAssetURL     string    `json:"target_asset_url"`
	TargetChecksumsURL string    `json:"target_checksums_url"`
	SourceKind         string    `json:"source_kind"`
	SourceRoot         string    `json:"source_root"`
	OldCommit          string    `json:"old_commit,omitempty"`
	NewCommit          string    `json:"new_commit,omitempty"`
	OldBinaryBackup    string    `json:"old_binary_backup,omitempty"`
	PriorRunning       bool      `json:"prior_running"`
	StartedAt          time.Time `json:"started_at"`
	UpdatedAt          time.Time `json:"updated_at"`
	ErrorCode          string    `json:"error_code,omitempty"`
	ErrorMessage       string    `json:"error_message,omitempty"`
	NotificationStatus string    `json:"notification_status"`
	ChatID             int64     `json:"chat_id"`
	PanelMessageID     int       `json:"panel_message_id"`
}

func (j UpdateJob) Terminal() bool {
	switch j.Phase {
	case "succeeded", "rolled_back", "stuck", "failed":
		return true
	}
	return false
}
