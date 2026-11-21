package update

import (
	"errors"
	"io/fs"
)

// step is the update step that failed. categorize needs it: the same error text means different things in a check, a
// download and a restart.
type step int

const (
	stepCheck step = iota
	stepDownload
	stepRestart
)

// category is the only thing an updater failure may log. Paths, framework text and OS text stay out of the log.
type category string

const (
	catCheckFailed    category = "check-failed"
	catCancelled      category = "cancelled"
	catNotWritable    category = "not-writable"
	catDownloadFailed category = "download-failed"
	catRestartFailed  category = "restart-failed"
	catOther          category = "other"
)

// Plain sentences returned to the UI. They carry no cause; the cause is never shown.
const (
	msgUnavailable    = "Updates are not available in this build."
	msgBusy           = "Another update step is already running."
	msgRestartFirst   = "An update is ready. Restart the app to apply it first."
	msgNotReady       = "There is no downloaded update to restart into."
	msgCheckFirst     = "Check for updates first."
	msgCheckFailed    = "Could not check for updates. Try again later."
	msgDownloadFailed = "The update could not be downloaded or verified. Try again."
	msgNotWritable    = "The app folder is not writable, so the update cannot be installed here. Download the new version from the release page."
	msgRestartFailed  = "The app could not restart into the update. The update is still ready, so you can try Restart again."
	msgOther          = "The update failed. Try again later."
)

// message is the sentence for a failure category. A cancel is not a failure and has no sentence.
func (c category) message() string {
	switch c {
	case catCheckFailed:
		return msgCheckFailed
	case catNotWritable:
		return msgNotWritable
	case catDownloadFailed:
		return msgDownloadFailed
	case catRestartFailed:
		return msgRestartFailed
	case catOther:
		return msgOther
	}
	return ""
}

// categorize maps a failed step to its log category. cancelled is the Service's own cancel flag. For check and download
// it wins over the error text, so a cancelled install never reports a failure. For download, fs.ErrPermission on the
// staging step means the folder is not writable; any other error is a failed download or verification. The framework's
// error text is not read beyond that sentinel.
func categorize(st step, cancelled bool, err error) category {
	switch st {
	case stepCheck:
		if cancelled {
			return catCancelled
		}
		return catCheckFailed
	case stepDownload:
		if cancelled {
			return catCancelled
		}
		if errors.Is(err, fs.ErrPermission) {
			return catNotWritable
		}
		return catDownloadFailed
	case stepRestart:
		return catRestartFailed
	}
	return catOther
}
