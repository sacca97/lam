package app

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

// How often the window looks for a newer release.
const updateCheckEvery = 24 * time.Hour

// updateChecksOn says whether the window may look for a newer release: it asks
// GitHub's public release list and sends nothing about the person.
func updateChecksOn(s uiSettings) bool {
	return !s.NoUpdateCheck && os.Getenv("LAM_UPDATE_CHECK") != "0"
}

type updateRecord struct {
	CheckedAt time.Time `json:"checkedAt"`
	Tag       string    `json:"tag,omitempty"`
}

func updateRecordPath() string { return filepath.Join(stateDir(), "update-check.json") }

func readUpdateRecord() updateRecord {
	var r updateRecord
	if data, err := os.ReadFile(updateRecordPath()); err == nil {
		_ = json.Unmarshal(data, &r)
	}
	return r
}

func writeUpdateRecord(r updateRecord) {
	if data, err := json.Marshal(r); err == nil {
		_ = os.MkdirAll(stateDir(), 0o755)
		_ = writeFileAtomic(updateRecordPath(), data, 0o644)
	}
}

// checkForUpdate asks for the latest release and says whether it is newer than
// this build. A development build never counts as out of date.
func checkForUpdate() (r release, newer bool, err error) {
	r, err = fetchRelease("")
	if err != nil {
		return release{}, false, err
	}
	order, ok := compareRelease(version, r.Tag)
	return r, ok && order < 0, nil
}
