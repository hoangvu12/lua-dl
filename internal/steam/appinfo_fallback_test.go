package steam

import (
	"context"
	"testing"
	"time"
)

func TestFetchFromMirrorsCS2(t *testing.T) {
	if testing.Short() {
		t.Skip("network")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	info, err := fetchFromMirrors(ctx, 730)
	if err != nil {
		t.Fatalf("fetchFromMirrors(730): %v", err)
	}
	if info.Name == "" {
		t.Errorf("empty name")
	}
	if len(info.Depots) == 0 {
		t.Errorf("no depots parsed")
	}
	var saw732 bool
	for _, d := range info.Depots {
		if d.DepotID == 732 {
			saw732 = true
			if len(d.OSList) == 0 || d.OSList[0] != "windows" {
				t.Errorf("depot 732 OSList = %v, want [windows]", d.OSList)
			}
		}
	}
	if !saw732 {
		t.Errorf("depot 732 missing")
	}
	t.Logf("ok: name=%q depots=%d", info.Name, len(info.Depots))
}

func TestFetchFromStoreAPI(t *testing.T) {
	if testing.Short() {
		t.Skip("network")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	info, err := fetchFromStoreAPI(ctx, 2413950)
	if err != nil {
		t.Fatalf("fetchFromStoreAPI: %v", err)
	}
	if info.Name != "Final Sentence" {
		t.Errorf("name = %q, want Final Sentence", info.Name)
	}
}
