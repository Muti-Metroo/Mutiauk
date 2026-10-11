//go:build windows && amd64

package tun

import _ "embed"

// wintunDLLBytes is the official Wintun 0.14.1 amd64 driver DLL, embedded
// so the binary is self-contained. It is extracted and SHA-256-verified
// at runtime by ensureWintunDLL before loading.
//
//go:embed wintun/amd64/wintun.dll
var wintunDLLBytes []byte

// wintunDLLSHA256 is the SHA-256 of the embedded amd64 wintun.dll.
const wintunDLLSHA256 = "e5da8447dc2c320edc0fc52fa01885c103de8c118481f683643cacc3220dafce"
