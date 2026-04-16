package workflows

import (
	"crs/config"
	"fmt"
	"log/slog"
	"os/exec"
	"runtime"
)

// notifyPRAdded sends a macOS desktop notification when a PR is added to a section.
// Does nothing if MacOSNotifications is disabled or the OS is not macOS.
func notifyPRAdded(log *slog.Logger, sectionName, prTitle string) {
	if !config.C().MacOSNotifications {
		return
	}
	if runtime.GOOS != "darwin" {
		return
	}

	message := fmt.Sprintf("PR Added to Section %s - %s", sectionName, prTitle)
	script := fmt.Sprintf(`display notification %q with title "Code Review Server"`, message)
	if err := exec.Command("osascript", "-e", script).Run(); err != nil {
		log.Warn("Failed to send macOS notification", "error", err)
	}
}
