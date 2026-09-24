package domain

import "time"

// UpdateCheck is the result shown by the Telegram options panel.
type UpdateCheck struct {
	Installed    string
	Running      string
	Release      Release
	Available    bool
	Blocker      string
	BlockerCode  string
	Checksum     string
	IntentID     string
	Installation PluginInstallation
	Checkout     CheckoutState
}

// UpdateIntent binds the second press to the exact panel, operator, release,
// installation and time window checked on the first press.
type UpdateIntent struct {
	ID             string
	ChatID         int64
	PanelMessageID int
	OperatorID     int64
	Tag            string
	Fingerprint    string
	ExpiresAt      time.Time
}
