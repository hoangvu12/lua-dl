// Package goldberg applies the Goldberg Steam Emulator (gbe_fork) to a
// freshly-downloaded depot so the game can run without a Steam account.
//
// Flow:
//  1. Scan all .exe/.dll files in the game dir for Steam API interface strings
//  2. Download + cache the latest gbe_fork Windows release (emu-win-release.7z)
//  3. For every steam_api.dll / steam_api64.dll found, replace it with the
//     emulator DLL and write a steam_settings/ config folder next to it
package goldberg

import (
	"context"
	"fmt"

	"github.com/hoangvu12/lua-dl/internal/ui"
)

// Offer applies the Goldberg Steam Emulator automatically — no prompt.
// Called when online-fix is either unavailable or declined by the user.
// Best-effort: errors are printed but never bubble up.
func Offer(ctx context.Context, appID uint32, gameDir string) error {
	ui.Phase("Goldberg Steam Emulator")
	if err := apply(ctx, appID, gameDir); err != nil {
		ui.LastStep(fmt.Sprintf("failed: %v", err))
	}
	return nil
}
