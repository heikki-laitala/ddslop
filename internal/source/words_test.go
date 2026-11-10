package source

import (
	"fmt"
	"testing"
)

func TestWordsCountsIdentifierLikeWordsEverywhere(t *testing.T) {
	src := `/// Calls load_user, see [load_user].
#[serde(default = "default_port")]
fn load_user(id: u64) -> r#type { format!("{}", id2x) }
`
	got := Words([]byte(src))
	want := map[string]int{"load_user": 3, "default_port": 1, "id": 1, "id2x": 1, "u64": 1, "type": 1, "fn": 1, "r": 1}
	for w, n := range want {
		if got[w] != n {
			t.Errorf("Words[%q] = %d, want %d (all: %v)", w, got[w], n, fmt.Sprint(got))
		}
	}
	if got["2x"] != 0 || got["64"] != 0 {
		t.Errorf("words cannot start with a digit: %v", got)
	}
}
