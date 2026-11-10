package rust

import "testing"

func testFlags(t *testing.T, path, src string) map[string]bool {
	t.Helper()
	units, err := Units(path, []byte(src))
	if err != nil {
		t.Fatal(err)
	}
	flags := map[string]bool{}
	for _, u := range units {
		flags[u.Name] = u.Test
	}
	return flags
}

func TestTestFunctionsAreMarked(t *testing.T) {
	src := `fn production() {}

#[test]
fn plain_test() {}

#[tokio::test(flavor = "multi_thread")]
async fn async_test() {}

#[rstest]
#[case(1)]
fn parametrised(#[case] n: u32) {}

#[inline]
/// Docs between attributes and the function do not matter.
#[must_use]
fn attributed_production() -> u32 { 1 }

#[cfg(test)]
mod tests {
    fn helper() {}

    mod nested {
        fn deeper() {}
    }
}

#[cfg(not(test))]
mod real {
    fn not_a_test() {}
}
`
	got := testFlags(t, "src/lib.rs", src)
	want := map[string]bool{
		"production":            false,
		"plain_test":            true,
		"async_test":            true,
		"parametrised":          true,
		"attributed_production": false,
		"helper":                true,
		"deeper":                true,
		"not_a_test":            false,
	}
	for name, w := range want {
		g, ok := got[name]
		if !ok {
			t.Errorf("no unit %q", name)
			continue
		}
		if g != w {
			t.Errorf("%s: Test = %v, want %v", name, g, w)
		}
	}
}

func TestFunctionsInTestFilesAreMarked(t *testing.T) {
	src := "fn helper() {}\n"
	for path, want := range map[string]bool{
		"crates/a/tests/integration.rs":     true,
		"crates/a/benches/speed.rs":         true,
		"crates/a/src/operational/tests.rs": true,
		"crates/a/src/lib.rs":               false,
		"crates/a/src/contests.rs":          false,
	} {
		if got := testFlags(t, path, src)["helper"]; got != want {
			t.Errorf("%s: Test = %v, want %v", path, got, want)
		}
	}
}
