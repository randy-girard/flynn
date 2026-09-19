package router

import "strings"

// TCP TLS modes. Empty / off is a plaintext TCP proxy (the historical default).
const (
	TLSModeOff         = ""
	TLSModePassthrough = "passthrough"
	TLSModeTerminate   = "terminate"
)

// NormalizeTLSMode maps CLI/API aliases onto the stored tls_mode values.
func NormalizeTLSMode(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "off", "none", "plain", "tcp":
		return TLSModeOff
	case "passthrough", "pass-through", "pass":
		return TLSModePassthrough
	case "terminate", "terminating":
		return TLSModeTerminate
	default:
		return strings.ToLower(strings.TrimSpace(s))
	}
}

// ValidTLSMode reports whether s is a known TCP TLS mode after normalization.
func ValidTLSMode(s string) bool {
	switch NormalizeTLSMode(s) {
	case TLSModeOff, TLSModePassthrough, TLSModeTerminate:
		return true
	default:
		return false
	}
}

// TerminatesTLS is true when the router should wrap the TCP listener in TLS.
func TerminatesTLS(mode string) bool {
	return NormalizeTLSMode(mode) == TLSModeTerminate
}
