package goldberg

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/bodgit/sevenzip"

	"github.com/hoangvu12/lua-dl/internal/ui"
)

const (
	gbeRepo  = "Detanup01/gbe_fork"
	gbeAsset = "emu-win-release.7z"
)

type ghAsset struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
}

type ghRelease struct {
	TagName string    `json:"tag_name"`
	Assets  []ghAsset `json:"assets"`
}

func apply(ctx context.Context, appID uint32, gameDir string) error {
	ui.Step("scanning game binaries for Steam interfaces")
	ifaces, err := scanInterfaces(gameDir)
	if err != nil {
		return fmt.Errorf("scan: %w", err)
	}

	dlls, err := findSteamAPIDLLs(gameDir)
	if err != nil {
		return fmt.Errorf("find dlls: %w", err)
	}
	if len(dlls) == 0 {
		return fmt.Errorf("no steam_api.dll or steam_api64.dll found in game directory")
	}

	ui.Step("fetching Goldberg emulator (gbe_fork)")
	gbeDir, err := fetchGBE(ctx)
	if err != nil {
		return fmt.Errorf("fetch gbe: %w", err)
	}

	for _, dllPath := range dlls {
		if err := applyToLocation(gbeDir, dllPath, appID, ifaces); err != nil {
			return fmt.Errorf("apply to %s: %w", filepath.Base(filepath.Dir(dllPath)), err)
		}
	}

	ui.LastStep(fmt.Sprintf("applied to %d location(s) · %d interface(s) detected",
		len(dlls), len(ifaces)))
	return nil
}

// fetchGBE downloads (and caches) the gbe_fork Windows release, returning
// the directory that contains the extracted steam_api*.dll files.
func fetchGBE(ctx context.Context) (string, error) {
	cacheBase, err := os.UserCacheDir()
	if err != nil {
		cacheBase = os.TempDir()
	}

	rel, err := latestRelease(ctx)
	if err != nil {
		return "", fmt.Errorf("github: %w", err)
	}

	cacheDir := filepath.Join(cacheBase, "lua-dl", "gbe", rel.TagName)
	if allDLLsCached(cacheDir) {
		return cacheDir, nil
	}

	var assetURL string
	for _, a := range rel.Assets {
		if a.Name == gbeAsset {
			assetURL = a.BrowserDownloadURL
			break
		}
	}
	if assetURL == "" {
		return "", fmt.Errorf("asset %s not found in release %s", gbeAsset, rel.TagName)
	}

	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		return "", err
	}

	err = downloadAndExtract(ctx, assetURL, cacheDir)
	if err == nil {
		return cacheDir, nil
	}

	if !isDefenderError(err) {
		return "", err
	}

	// Defender quarantined the archive — request an exclusion via UAC and retry.
	_ = os.Remove(filepath.Join(cacheDir, gbeAsset))
	ui.Step("Windows Defender blocked the download · click Yes in the popup to fix")
	if exclErr := addDefenderExclusion(cacheDir); exclErr != nil {
		return "", fmt.Errorf(
			"auto-fix failed — add this folder to Defender exclusions manually and retry:\n     %s", cacheDir)
	}
	ui.Step("exclusion added · re-downloading (once only — cached after this)")

	if err := downloadAndExtract(ctx, assetURL, cacheDir); err != nil {
		return "", fmt.Errorf("retry after exclusion: %w", err)
	}
	return cacheDir, nil
}

func downloadAndExtract(ctx context.Context, assetURL, cacheDir string) error {
	archivePath := filepath.Join(cacheDir, gbeAsset)
	if err := downloadFile(ctx, assetURL, archivePath); err != nil {
		_ = os.Remove(archivePath)
		return fmt.Errorf("download: %w", err)
	}
	if err := extractDLLs(archivePath, cacheDir); err != nil {
		return fmt.Errorf("extract: %w", err)
	}
	_ = os.Remove(archivePath)
	return nil
}

func allDLLsCached(dir string) bool {
	for _, name := range []string{"steam_api.dll", "steam_api64.dll"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			return false
		}
	}
	return true
}

func latestRelease(ctx context.Context) (*ghRelease, error) {
	req, err := http.NewRequestWithContext(ctx, "GET",
		"https://api.github.com/repos/"+gbeRepo+"/releases/latest", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("GitHub API: HTTP %d", resp.StatusCode)
	}
	var rel ghRelease
	if err := json.NewDecoder(resp.Body).Decode(&rel); err != nil {
		return nil, err
	}
	return &rel, nil
}

func downloadFile(ctx context.Context, url, destPath string) error {
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	f, err := os.Create(destPath)
	if err != nil {
		return err
	}
	defer f.Close()

	var total uint64
	if resp.ContentLength > 0 {
		total = uint64(resp.ContentLength)
	}
	pr := ui.NewProgressReader(resp.Body, total, gbeAsset)
	_, err = io.Copy(f, pr)
	pr.Done()
	return err
}

// extractDLLs pulls steam_api.dll and steam_api64.dll out of the 7z archive
// into destDir. It takes the first match for each name regardless of path depth.
func extractDLLs(archivePath, destDir string) error {
	r, err := sevenzip.OpenReader(archivePath)
	if err != nil {
		return err
	}
	defer r.Close()

	// false = not yet extracted, absence = not wanted
	needed := map[string]bool{
		"steam_api.dll":   false,
		"steam_api64.dll": false,
	}

	for _, f := range r.File {
		if f.FileInfo().IsDir() {
			continue
		}
		name := strings.ToLower(filepath.Base(filepath.FromSlash(f.Name)))
		done, want := needed[name]
		if !want || done {
			continue
		}

		rc, err := f.Open()
		if err != nil {
			return fmt.Errorf("open %s in archive: %w", f.Name, err)
		}
		outf, err := os.Create(filepath.Join(destDir, name))
		if err != nil {
			rc.Close()
			return err
		}
		_, copyErr := io.Copy(outf, rc)
		outf.Close()
		rc.Close()
		if copyErr != nil {
			return copyErr
		}
		needed[name] = true
	}

	for name, found := range needed {
		if !found {
			return fmt.Errorf("%s not found in archive", name)
		}
	}
	return nil
}

// applyToLocation backs up the original DLL, replaces it with Goldberg's
// version, and writes steam_settings/ config files next to it.
func applyToLocation(gbeDir, dllPath string, appID uint32, ifaces []string) error {
	dllName := strings.ToLower(filepath.Base(dllPath))
	srcDLL := filepath.Join(gbeDir, dllName)
	if _, err := os.Stat(srcDLL); err != nil {
		return fmt.Errorf("goldberg %s not found in cache", dllName)
	}

	// Backup original
	backupPath := dllPath + ".bak"
	if err := os.Rename(dllPath, backupPath); err != nil {
		return fmt.Errorf("backup original: %w", err)
	}

	if err := copyFile(srcDLL, dllPath); err != nil {
		_ = os.Rename(backupPath, dllPath) // restore on failure
		return fmt.Errorf("copy emulator dll: %w", err)
	}

	settingsDir := filepath.Join(filepath.Dir(dllPath), "steam_settings")
	if err := os.MkdirAll(settingsDir, 0o755); err != nil {
		return err
	}

	if err := os.WriteFile(
		filepath.Join(settingsDir, "steam_appid.txt"),
		[]byte(fmt.Sprintf("%d\n", appID)),
		0o644,
	); err != nil {
		return err
	}

	if len(ifaces) > 0 {
		if err := os.WriteFile(
			filepath.Join(settingsDir, "steam_interfaces.txt"),
			[]byte(strings.Join(ifaces, "\n")+"\n"),
			0o644,
		); err != nil {
			return err
		}
	}

	return nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, in)
	return err
}
