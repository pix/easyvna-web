package web

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io/fs"
	"testing"
)

// The native program serves the flasher and the bundled firmware.
func TestStaticHasFlasherAndFirmware(t *testing.T) {
	st := Static()
	for _, f := range []string{"index.html", "app.js", "flasher.js", "flasher.css"} {
		if _, err := fs.Stat(st, f); err != nil {
			t.Error(err)
		}
	}
	raw, err := fs.ReadFile(st, "firmware/manifest.json")
	if err != nil {
		t.Fatal(err)
	}
	var m struct {
		File, SHA256, Version, Source, License string
		Size, FlashSize                        int
	}
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	bin, err := fs.ReadFile(st, "firmware/"+m.File)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(bin)
	if hex.EncodeToString(sum[:]) != m.SHA256 || len(bin) != m.Size {
		t.Fatalf("embedded %s does not match its manifest", m.File)
	}
	if m.FlashSize != 256*1024 || m.License != "GPL-3.0" || m.Version == "" || m.Source == "" {
		t.Fatalf("manifest incomplete: %+v", m)
	}
}
