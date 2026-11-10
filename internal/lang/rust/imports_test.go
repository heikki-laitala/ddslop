package rust

import (
	"fmt"
	"maps"
	"slices"
	"testing"
)

func TestImportsMapsLocalNamesToPaths(t *testing.T) {
	src := `use std::num::NonZeroU64;
use crate::outage::StoreFault;
use sha2::{Digest, Sha256};
use token_credentials::{decode as decode_token, is_canonical_key_id};
use tokio::sync::mpsc::channel;
use super::helpers::{self, fill};
use serde::*;
pub use acme_traces::subscriber;
use tracing;

mod inner {
    use x::y;
}
`
	got := Imports([]byte(src))

	want := map[string]string{ //nolint:gosec // crate and function names, not credentials
		"NonZeroU64":          "std::num::NonZeroU64",
		"StoreFault":          "crate::outage::StoreFault",
		"Digest":              "sha2::Digest",
		"Sha256":              "sha2::Sha256",
		"decode_token":        "token_credentials::decode",
		"is_canonical_key_id": "token_credentials::is_canonical_key_id",
		"channel":             "tokio::sync::mpsc::channel",
		"helpers":             "super::helpers",
		"fill":                "super::helpers::fill",
		"subscriber":          "acme_traces::subscriber",
		"tracing":             "tracing",
		"y":                   "x::y",
	}
	if !maps.Equal(got, want) {
		keys := slices.Sorted(maps.Keys(got))
		var lines []string
		for _, k := range keys {
			lines = append(lines, k+"="+got[k])
		}
		t.Errorf("Imports = %v\nwant %v", lines, fmt.Sprint(want))
	}
}
