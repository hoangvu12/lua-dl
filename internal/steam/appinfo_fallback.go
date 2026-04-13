package steam

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// appInfoMirrors mirrors internal/resolver.Mirrors — kept in sync manually.
// These repos use one branch per app, each containing {appid}.json with a
// PICS product info dump (name, per-depot oslist, language, dlcappid, etc).
var appInfoMirrors = []string{
	"SPIN0ZAi/SB_manifest_DB",
	"tymolu233/ManifestAutoUpdate-fix",
	"BlankTMing/ManifestAutoUpdate",
	"Auiowu/ManifestAutoUpdate",
	"pjy612/SteamManifestCache",
}

// FetchAppInfo returns app metadata via a three-tier fallback:
//  1. anonymous PICS (works for free/server apps)
//  2. community JSON mirrors on raw.githubusercontent.com (paid games that
//     someone has already indexed — full per-depot metadata)
//  3. Steam Store API (name + type only, for rare apps not in any mirror)
//
// The lua file drives the actual download; this is only for folder naming
// and picker classification (core/locale/DLC/wrong-OS).
func FetchAppInfo(ctx context.Context, client *Client, appID uint32) (*AppInfo, error) {
	if info, err := client.GetAppInfo(appID); err == nil {
		return info, nil
	}
	if info, err := fetchFromMirrors(ctx, appID); err == nil {
		return info, nil
	}
	if info, err := fetchFromStoreAPI(ctx, appID); err == nil {
		return info, nil
	}
	return nil, fmt.Errorf("no metadata source returned info for %d", appID)
}

type mirrorJSON struct {
	Name  string                     `json:"name"`
	Depot map[string]json.RawMessage `json:"depot"`
}

type mirrorDepotConfig struct {
	OSList      string `json:"oslist"`
	OSArch      string `json:"osarch"`
	Language    string `json:"language"`
	LowViolence string `json:"lowviolence"`
}

type mirrorDepotManifest struct {
	GID  string `json:"gid"`
	Size string `json:"size"`
}

type mirrorDepot struct {
	Name      string                         `json:"name"`
	Config    mirrorDepotConfig              `json:"config"`
	DLCAppID  string                         `json:"dlcappid"`
	Manifests map[string]mirrorDepotManifest `json:"manifests"`
}

func fetchFromMirrors(ctx context.Context, appID uint32) (*AppInfo, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	type result struct {
		info *AppInfo
		err  error
	}
	out := make(chan result, len(appInfoMirrors))
	var wg sync.WaitGroup
	for _, repo := range appInfoMirrors {
		wg.Add(1)
		go func(repo string) {
			defer wg.Done()
			url := fmt.Sprintf("https://raw.githubusercontent.com/%s/%d/%d.json", repo, appID, appID)
			info, err := fetchMirrorJSON(ctx, url, appID)
			if err != nil {
				out <- result{err: err}
				return
			}
			out <- result{info: info}
		}(repo)
	}
	go func() { wg.Wait(); close(out) }()
	var lastErr error
	for r := range out {
		if r.info != nil {
			cancel()
			return r.info, nil
		}
		lastErr = r.err
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("no mirror has %d.json", appID)
	}
	return nil, lastErr
}

func fetchMirrorJSON(ctx context.Context, url string, appID uint32) (*AppInfo, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return nil, fmt.Errorf("HTTP %d", res.StatusCode)
	}
	body, err := io.ReadAll(res.Body)
	if err != nil {
		return nil, err
	}

	var raw mirrorJSON
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, err
	}
	info := &AppInfo{AppID: appID, Name: raw.Name}
	if info.Name == "" {
		info.Name = "app-" + strconv.FormatUint(uint64(appID), 10)
	}
	for k, v := range raw.Depot {
		id, err := strconv.ParseUint(k, 10, 32)
		if err != nil {
			continue // skip "baselanguages", "workshopdepot", etc.
		}
		var d mirrorDepot
		if err := json.Unmarshal(v, &d); err != nil {
			continue // scalar entries like "overridescddb":"1"
		}
		depot := Depot{
			DepotID:  uint32(id),
			Name:     d.Name,
			Language: d.Config.Language,
			OSArch:   d.Config.OSArch,
		}
		if d.Config.OSList != "" {
			depot.OSList = strings.Split(d.Config.OSList, ",")
		}
		if d.Config.LowViolence == "1" || d.Config.LowViolence == "true" {
			depot.LowViolence = true
		}
		if n, err := strconv.ParseUint(d.DLCAppID, 10, 32); err == nil {
			depot.DLCAppID = uint32(n)
		}
		if pub, ok := d.Manifests["public"]; ok {
			if n, err := strconv.ParseUint(pub.GID, 10, 64); err == nil {
				depot.ManifestID = n
			}
			if n, err := strconv.ParseUint(pub.Size, 10, 64); err == nil {
				depot.MaxSize = n
			}
		}
		info.Depots = append(info.Depots, depot)
	}
	return info, nil
}

type storeDetailsEntry struct {
	Success bool `json:"success"`
	Data    struct {
		Name string `json:"name"`
		Type string `json:"type"`
	} `json:"data"`
}

func fetchFromStoreAPI(ctx context.Context, appID uint32) (*AppInfo, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	url := fmt.Sprintf("https://store.steampowered.com/api/appdetails?appids=%d&cc=us&l=en", appID)
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return nil, fmt.Errorf("HTTP %d", res.StatusCode)
	}
	body, err := io.ReadAll(res.Body)
	if err != nil {
		return nil, err
	}
	var r map[string]storeDetailsEntry
	if err := json.Unmarshal(body, &r); err != nil {
		return nil, err
	}
	entry, ok := r[strconv.FormatUint(uint64(appID), 10)]
	if !ok || !entry.Success {
		return nil, fmt.Errorf("store API: no success for %d", appID)
	}
	name := entry.Data.Name
	if name == "" {
		name = "app-" + strconv.FormatUint(uint64(appID), 10)
	}
	return &AppInfo{AppID: appID, Name: name}, nil
}
