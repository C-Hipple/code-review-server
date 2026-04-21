package workflows

import (
	"fmt"
	"log/slog"
	"os/exec"
	"runtime"
)

// notifyPRAdded sends a desktop notification when a PR is added to a section.
// Currently only macOS (osascript) is supported. The caller is responsible for
// deciding whether notifications should fire (global or per-workflow override).
func notifyPRAdded(log *slog.Logger, sectionName, prTitle string) {
	switch runtime.GOOS {
	case "darwin":
		message := fmt.Sprintf("PR Added to Section %s - %s", sectionName, prTitle)
		script := fmt.Sprintf(`display notification %q with title "Code Review Server"`, message)
		if err := exec.Command("osascript", "-e", script).Run(); err != nil {
			log.Warn("Failed to send macOS notification", "error", err)
		}
	default:
		log.Error("DesktopNotifications is enabled but is not supported on this OS", "os", runtime.GOOS)
	}
}
