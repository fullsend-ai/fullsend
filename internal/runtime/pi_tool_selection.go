package runtime

import (
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"fmt"
)

const piToolSelectionExtensionFile = "fullsend-tools.js"
const piToolSelectionExit = 92

//go:embed pi_extension/fullsend-tools.js
var piToolSelectionExtensionJS []byte

func piToolSelectionGuard(path string) string {
	return fmt.Sprintf(`{ test -f %s && [ "$(command -p sha256sum %s | command -p cut -d' ' -f1)" = %s ] || { echo 'fullsend: pi tool selector missing or modified; refusing to run' >&2; exit %d; }; }`, shellQuote(path), shellQuote(path), shellQuote(piToolSelectionDigest()), piToolSelectionExit)
}

func piToolSelectionDigest() string {
	sum := sha256.Sum256(piToolSelectionExtensionJS)
	return hex.EncodeToString(sum[:])
}
