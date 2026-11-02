package market

import (
	_ "embed"
	"encoding/json"
	"log/slog"
)

//go:embed builtin_catalog.json
var builtinCatalog []byte

// Builtin returns the catalogue shipped inside the binary. It always loads:
// a corrupted embedded file is a build-time bug, not a runtime condition.
func Builtin() []Entry {
	var idx Index
	if err := json.Unmarshal(builtinCatalog, &idx); err != nil {
		slog.Default().Error("market: embedded catalogue is invalid", "error", err)
		return nil
	}
	entries := idx.Plugins
	for i := range entries {
		entries[i].Source = "builtin"
	}
	return entries
}
