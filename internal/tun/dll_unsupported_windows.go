//go:build windows && !amd64 && !arm64

package tun

// Mutiauk ships the embedded Wintun driver only for amd64 and arm64,
// the two Windows architectures it releases for. On any other Windows
// architecture there is no embedded DLL; ensureWintunDLL fails loudly.
var wintunDLLBytes []byte

const wintunDLLSHA256 = ""
