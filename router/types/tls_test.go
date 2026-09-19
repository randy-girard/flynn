package router

import "testing"

func TestNormalizeTLSMode(t *testing.T) {
	cases := map[string]string{
		"":              TLSModeOff,
		"off":           TLSModeOff,
		"PLAIN":         TLSModeOff,
		"passthrough":   TLSModePassthrough,
		"pass-through":  TLSModePassthrough,
		"terminate":     TLSModeTerminate,
		" TERMINATING ": TLSModeTerminate,
	}
	for in, want := range cases {
		if got := NormalizeTLSMode(in); got != want {
			t.Errorf("NormalizeTLSMode(%q)=%q want %q", in, got, want)
		}
	}
	if ValidTLSMode("bogus") {
		t.Fatal("bogus mode must be invalid")
	}
	if !TerminatesTLS("terminate") || TerminatesTLS("passthrough") || TerminatesTLS("") {
		t.Fatal("TerminatesTLS mismatch")
	}
}
