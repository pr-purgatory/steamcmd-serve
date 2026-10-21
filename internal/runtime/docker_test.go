package runtime

import "testing"

func TestANSIStripped(t *testing.T) {
	in := "\x1b[0m Update state (0x61) downloading\x1b[1;31m!\x1b[?25h"
	if got := ansi.ReplaceAllString(in, ""); got != " Update state (0x61) downloading!" {
		t.Fatalf("got %q", got)
	}
}
