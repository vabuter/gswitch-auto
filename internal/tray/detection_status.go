package tray

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
)

// TrayStatus represents the overall tray icon status.
type TrayStatus int

const (
	// TrayStatusOK - normal operation, layout switch detected or explicitly configured.
	TrayStatusOK TrayStatus = iota
	// TrayStatusNeedsConfig - autodetect did not find layout switch config.
	TrayStatusNeedsConfig
	// TrayStatusServiceError - gswitch service is not running or failed.
	TrayStatusServiceError
	// TrayStatusDetectError - error occurred during detection.
	TrayStatusDetectError
)

// DetectionInfo contains information about the detection status.
type DetectionInfo struct {
	Status         TrayStatus
	Source         string   // "xkb", "gnome", etc.
	KeyNames       string   // "Alt+Shift", "Super+Space", etc.
	Error          string   // Error message if any
	ErrorMessageID string   // Own status messages are translated at display time.
	Warning        string   // Warning message if any
	Attempts       []string // Provider attempts for diagnostics
	// Scancodes are the detected (or configured) layout switch keys; the
	// settings window checks hotkey chords against them.
	Scancodes []uint16
}

// detectJSONOutput mirrors the JSON structure from gswitch --detect-layout-switch.
// We use a local struct to avoid importing main package.
type detectJSONOutput struct {
	Schema   int                 `json:"schema"`
	Status   string              `json:"status"`
	Result   *detectJSONResult   `json:"result"`
	Error    string              `json:"error,omitempty"`
	Warning  string              `json:"warning,omitempty"`
	Attempts []detectJSONAttempt `json:"attempts"`
}

type detectJSONResult struct {
	Scancodes []uint16 `json:"scancodes"`
	Source    string   `json:"source"`
	Raw       string   `json:"raw"`
	Keys      string   `json:"keys"`
}

type detectJSONAttempt struct {
	Provider string `json:"provider"`
	Status   string `json:"status"`
	Raw      string `json:"raw,omitempty"`
	Keys     string `json:"keys,omitempty"`
	Error    string `json:"error,omitempty"`
}

// CheckDetectionStatus determines the current detection status.
// It checks service status first (if systemctl is available), then runs detection if needed.
func CheckDetectionStatus(sm *ServiceManager) DetectionInfo {
	if info, ok := forcedDetectionStatus(); ok {
		return info
	}

	info := DetectionInfo{}

	// 1. Check service status (only if systemctl is available)
	// Skip service check in non-systemd environments to allow detection to work
	if systemctlAvailable() {
		status, _ := sm.GetStatus()

		// Only block on Failed or Stopped - these indicate a real problem.
		// StatusNotInstalled means systemd service doesn't exist, but user may
		// run gswitch manually or be in an environment without full systemd support
		// (e.g., WSL, containers). In that case, continue to CLI detection.
		if status == StatusFailed || status == StatusStopped {
			info.Status = TrayStatusServiceError
			switch status {
			case StatusFailed:
				info.ErrorMessageID = strTooltipServiceFailed
				info.Error = tr(strTooltipServiceFailed)
			case StatusStopped:
				info.ErrorMessageID = strTooltipServiceStopped
				info.Error = tr(strTooltipServiceStopped) // "service is not running" for a clear tooltip
			}
			return info
		}
	}

	// 2. Check config - if not "auto", we consider it OK
	cfg := LoadTrayConfig()
	if cfg.LayoutSwitch != "auto" {
		info.Status = TrayStatusOK
		info.KeyNames = formatKeyValue(cfg.LayoutSwitch)
		info.Source = "config"
		info.Scancodes = layoutSwitchScancodes(cfg.LayoutSwitch)
		return info
	}

	// 3. Run detection via CLI
	return runDetection()
}

// runDetection runs gswitch --detect-layout-switch and parses the result.
// Uses findGswitchBinary from gswitch_binary.go to locate the binary.
func runDetection() DetectionInfo {
	info := DetectionInfo{}

	gswitchPath, findErr := findGswitchBinary()
	if findErr != nil {
		info.Status = TrayStatusDetectError
		info.ErrorMessageID = strGswitchMissing
		info.Error = tr(strGswitchMissing)
		return info
	}

	// #nosec G204 - gswitch is a known binary found via findGswitchBinary
	cmd := exec.Command(gswitchPath, "--detect-layout-switch")
	output, err := cmd.Output()
	return detectionInfoFromOutput(output, err)
}

// detectionInfoFromOutput maps the JSON printed by gswitch
// --detect-layout-switch (and the command error) to DetectionInfo.
func detectionInfoFromOutput(output []byte, err error) DetectionInfo {
	info := DetectionInfo{}

	// Parse JSON regardless of exit code (exit code indicates status)
	var result detectJSONOutput
	if jsonErr := json.Unmarshal(output, &result); jsonErr != nil {
		// JSON parse error - likely gswitch not found or crashed
		info.Status = TrayStatusDetectError
		info.Error = formatDetectionError(err)
		return info
	}

	// Convert attempts for diagnostics
	for _, att := range result.Attempts {
		attemptStr := att.Provider + ": " + att.Status
		if att.Error != "" {
			attemptStr += " (" + att.Error + ")"
		}
		info.Attempts = append(info.Attempts, attemptStr)
	}

	// Process status
	switch result.Status {
	case "found":
		info.Status = TrayStatusOK
		if result.Result != nil {
			info.Source = result.Result.Source
			info.KeyNames = result.Result.Keys
			info.Scancodes = append([]uint16(nil), result.Result.Scancodes...)
		}
		info.Warning = result.Warning
	case "not_found":
		info.Status = TrayStatusNeedsConfig
	default:
		info.Status = TrayStatusDetectError
		info.Error = result.Error
	}

	return info
}

// layoutSwitchScancodes parses an explicit layout-switch value ("56+42",
// "58"). It returns nil for "auto", "custom" and anything unparsable.
func layoutSwitchScancodes(value string) []uint16 {
	value = strings.TrimSpace(value)
	if value == "" || value == "auto" || value == "custom" {
		return nil
	}
	parts := strings.Split(value, "+")
	codes := make([]uint16, 0, len(parts))
	for _, part := range parts {
		code, err := strconv.ParseUint(strings.TrimSpace(part), 10, 16)
		if err != nil || code == 0 {
			return nil
		}
		codes = append(codes, uint16(code))
	}
	return codes
}

// formatDetectionError formats an error from running gswitch CLI into a user-friendly message.
func formatDetectionError(err error) string {
	if err == nil {
		return tr(strResponseInvalid)
	}
	// Check if binary not found
	if os.IsNotExist(err) || strings.Contains(err.Error(), "executable file not found") {
		return tr(strGswitchMissing)
	}
	return fmt.Sprintf(tr(strGswitchRunFailed), err.Error())
}

// desktopIsGNOME checks if the current desktop environment is GNOME.
// This is a local implementation for tray package to avoid importing main package.
// Uses os.Getenv() since tray runs as the user, not root.
func desktopIsGNOME() bool {
	// Check XDG_CURRENT_DESKTOP first (most reliable)
	currentDesktop := os.Getenv("XDG_CURRENT_DESKTOP")
	if containsIgnoreCase(currentDesktop, "gnome") {
		return true
	}

	// Check XDG_SESSION_DESKTOP
	sessionDesktop := os.Getenv("XDG_SESSION_DESKTOP")
	if containsIgnoreCase(sessionDesktop, "gnome") {
		return true
	}

	// Check DESKTOP_SESSION (legacy)
	desktopSession := os.Getenv("DESKTOP_SESSION")
	return containsIgnoreCase(desktopSession, "gnome")
}

// containsIgnoreCase checks if s contains substr (case-insensitive).
func containsIgnoreCase(s, substr string) bool {
	return strings.Contains(strings.ToLower(s), strings.ToLower(substr))
}

// systemctlAvailable checks if systemctl command is available in PATH.
// Used to skip service status check in non-systemd environments.
func systemctlAvailable() bool {
	_, err := exec.LookPath("systemctl")
	return err == nil
}

func (info DetectionInfo) errorText() string {
	if info.ErrorMessageID != "" {
		return tr(info.ErrorMessageID)
	}
	return info.Error
}
