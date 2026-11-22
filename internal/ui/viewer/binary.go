package viewer

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"strings"
)

// BinaryMIME marks content whose body is raw bytes. SetContent replaces
// such a body with BinaryPreview so the bytes never reach the terminal.
const BinaryMIME = "application/octet-stream"

// BinaryPreviewBytes bounds the hex dump. 4 KiB is 256 dump lines.
const BinaryPreviewBytes = 4096

// BinaryPreview describes raw bytes for a terminal: size, detected content
// type, SHA-256 (to check an F5 export against), and a hex dump of the
// first BinaryPreviewBytes bytes. The result is plain ASCII.
func BinaryPreview(data []byte) string {
	var b strings.Builder
	sum := sha256.Sum256(data)
	b.WriteString("Binary content, shown as a hex dump. F5 saves the original bytes.\n\n")
	fmt.Fprintf(&b, "Size:          %d bytes\n", len(data))
	fmt.Fprintf(&b, "Content type:  %s (detected)\n", http.DetectContentType(data))
	fmt.Fprintf(&b, "SHA-256:       %s\n", hex.EncodeToString(sum[:]))
	if len(data) == 0 {
		return b.String()
	}
	shown := data
	if len(shown) > BinaryPreviewBytes {
		shown = shown[:BinaryPreviewBytes]
	}
	b.WriteString("\n")
	b.WriteString(hex.Dump(shown))
	if rest := len(data) - len(shown); rest > 0 {
		fmt.Fprintf(&b, "... %d more bytes not shown\n", rest)
	}
	return b.String()
}
