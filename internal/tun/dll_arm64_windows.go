//go:build windows && arm64

package tun

import _ "embed"

// wintunDLLBytes is the official Wintun 0.14.1 arm64 driver DLL, embedded
// so the binary is self-contained. It is extracted and SHA-256-verified
// at runtime by ensureWintunDLL before loading.
//
//go:embed wintun/arm64/wintun.dll
var wintunDLLBytes []byte

// wintunDLLSHA256 is the SHA-256 of the embedded arm64 wintun.dll.
const wintunDLLSHA256 = "f7ba89005544be9d85231a9e0d5f23b2d15b3311667e2dad0debd344918a3f80"
