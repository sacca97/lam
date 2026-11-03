//go:build gui

package app

import (
	"image/png"
	"os"
	"strings"
	"testing"
)

func shot(t *testing.T, name string, g *guiApp) {
	img := renderGUI(t, g, 1240, 720, 1)
	dir := os.Getenv("GUI_SHOT_DIR")
	if dir == "" {
		dir = t.TempDir()
	}
	f, _ := os.Create(dir + "/" + name + ".png")
	png.Encode(f, img)
	f.Close()
}

// A newer release shows as a button in the footer and a row in Settings, and
// the dialog it opens asks before it replaces anything.
func TestGUIUpdateIsOffered(t *testing.T) {
	e := testPane(t, 100, 30)
	g := newTestApp(t, e)
	if _, ok := g.upd.available(); ok {
		t.Fatal("an update is offered before any check")
	}
	g.upd.tag = "v9.9.9"
	shot(t, "gui-update-footer", g)

	g.settings.NoUpdateCheck = false
	vals := g.settingValues()
	if !strings.HasPrefix(vals[settingVersion][0], "lam ") || vals[settingVersion][1] != "Update to v9.9.9" {
		t.Fatalf("version row = %q", vals[settingVersion])
	}
	g.dlg = dialog{kind: dlgSettings, row: settingVersion}
	shot(t, "gui-update-settings", g)

	g.openUpdate()
	if g.dlg.kind != dlgUpdate {
		t.Fatal("the update dialog did not open")
	}
	title, ok, _, body := g.updateDialogText()
	if title != "Update lam" || ok != "Update" || !strings.Contains(body[0], "v9.9.9") {
		t.Fatalf("dialog = %q %q %q", title, ok, body)
	}
	shot(t, "gui-update-dialog", g)

	g.upd.failed = "download lam-linux-amd64-desktop: 404 Not Found"
	shot(t, "gui-update-failed", g)
}
