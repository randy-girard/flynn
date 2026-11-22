package rfc5424

import "testing"

// Malformed frames reach the parser straight from the logaggregator syslog
// port, so it must return an error, never panic.
var malformedMessages = []string{
	"<0>1 0000-01-01T0:00:00Z ",
	"<0>1 2013-07-17T16:43:41Z ",
	"<0>1 2013-07-17T16:43:41Z host",
	"<0>1 2013-07-17T16:43:41Z host ",
	"<0>1 2013-07-17T16:43:41Z host app ",
	"<0>1 2013-07-17T16:43:41Z host app proc ",
	"<0>1 2013-07-17T16:43:41Z host app proc msgid ",
	"<0>1 2013-07-17T16:43:41Z host app proc msgid [x",
	"<0>",
	"<",
}

func TestParseMalformedDoesNotPanic(t *testing.T) {
	for _, m := range malformedMessages {
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Errorf("Parse(%q) panicked: %v", m, r)
				}
			}()
			if _, err := Parse([]byte(m)); err == nil {
				t.Errorf("Parse(%q): expected an error", m)
			}
		}()
	}
}

func FuzzParse(f *testing.F) {
	f.Add([]byte(`<134>1 2013-07-17T16:43:41Z host app web.1 ID1 [flynn seq="1"] hi`))
	f.Add([]byte(`<1>1 2013-07-17T16:43:41Z - - - - -`))
	for _, m := range malformedMessages {
		f.Add([]byte(m))
	}
	f.Fuzz(func(t *testing.T, b []byte) {
		Parse(b)
	})
}
