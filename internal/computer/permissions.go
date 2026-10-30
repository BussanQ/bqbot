package computer

// PermissionStatus reports OS permissions for the desktop process without prompting.
type PermissionStatus struct {
	Supported       bool `json:"supported"`
	Accessibility   bool `json:"accessibility"`
	ScreenRecording bool `json:"screen_recording"`
}
