package clone

import (
	"fmt"
	"strings"
	"testing"

	"github.com/heikki-laitala/ddslop/internal/unit"
)

// st builds a struct from "name:type" fields, one per line after start.
func st(file, name string, start int, fields ...string) unit.Struct {
	s := unit.Struct{File: file, Name: name, StartLine: start, EndLine: start + len(fields) + 1, Nodes: 10 * len(fields)}
	for i, f := range fields {
		n, t, _ := strings.Cut(f, ":")
		s.Fields = append(s.Fields, unit.Field{Name: n, Type: t, Line: start + 1 + i})
	}
	return s
}

func structGroupStrings(groups []StructGroup) string {
	var out []string
	for _, g := range groups {
		var members []string
		for _, s := range g.Structs {
			members = append(members, fmt.Sprintf("%s:%d %s", s.File, s.StartLine, s.Name))
		}
		out = append(out, strings.Join(members, " | "))
	}
	return strings.Join(out, "\n")
}

func TestStructsGroupsSameFieldNamesInAnyOrder(t *testing.T) {
	structs := []unit.Struct{
		st("b.rs", "VerifierFile", 5, "key_id:String", "digest:String", "state:State"),
		st("a.rs", "VerifierFile", 1, "digest:String", "state:State", "key_id:String"),
		st("c.rs", "Other", 1, "digest:String", "state:State", "owner:String"),
		st("d.rs", "Small", 1, "a:u8", "b:u8"),
		st("e.rs", "Small", 1, "a:u8", "b:u8"),
	}
	groups := Structs(structs, 3)
	if got, want := structGroupStrings(groups), "a.rs:1 VerifierFile | b.rs:5 VerifierFile"; got != want {
		t.Fatalf("groups:\n%s\nwant:\n%s", got, want)
	}
	if len(groups[0].Differences) != 0 {
		t.Errorf("identical structs have differences %+v", groups[0].Differences)
	}
}

func TestStructsListDifferingNamesAndFieldTypes(t *testing.T) {
	structs := []unit.Struct{
		st("a.rs", "Rates", 1, "input:u64", "output:u64", "cache_read:u64", "cache_write:u64"),
		st("b.rs", "AudioRates", 1, "input:u64", "output:u64", "cache_read:Option<u64>", "cache_write:u64"),
	}
	groups := Structs(structs, 3)
	if len(groups) != 1 {
		t.Fatalf("got %d groups, want 1", len(groups))
	}
	var got []string
	for _, d := range groups[0].Differences {
		got = append(got, fmt.Sprintf("%v %v %v local=%v", d.Kind, d.Values, d.Lines, d.Local))
	}
	want := []string{
		fmt.Sprintf("%v [Rates AudioRates] [[1] [1]] local=true", unit.Identifier),
		fmt.Sprintf("%v [u64 Option<u64>] [[4] [4]] local=false", unit.Type),
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("differences:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

func TestStructsOrderedByDuplicatedSize(t *testing.T) {
	structs := []unit.Struct{
		st("a.rs", "Small", 1, "a:u8", "b:u8", "c:u8"),
		st("b.rs", "Small", 1, "a:u8", "b:u8", "c:u8"),
		st("a.rs", "Big", 10, "a:u8", "b:u8", "c:u8", "d:u8"),
		st("b.rs", "Big", 10, "a:u8", "b:u8", "c:u8", "d:u8"),
	}
	got := structGroupStrings(Structs(structs, 3))
	want := "a.rs:10 Big | b.rs:10 Big\na.rs:1 Small | b.rs:1 Small"
	if got != want {
		t.Errorf("groups:\n%s\nwant:\n%s", got, want)
	}
}

func TestStructsSkipStructsConvertingFromOneAnother(t *testing.T) {
	port := st("spec.rs", "Port", 1, "host:i64", "container:i64", "name:String", "access:Access")
	port.Converts = []string{"AuthoredPort"}
	authored := st("spec.rs", "AuthoredPort", 20, "host:i64", "container:i64", "name:String", "access:String")
	copied := st("other.rs", "Port", 1, "host:i64", "container:i64", "name:String", "access:Access")

	if got := Structs([]unit.Struct{port, authored}, 3); len(got) != 0 {
		t.Errorf("a struct and the type it validates were reported: %s", structGroupStrings(got))
	}
	// A true copy of the validated struct is still reported.
	if got, want := structGroupStrings(Structs([]unit.Struct{port, authored, copied}, 3)), "other.rs:1 Port | spec.rs:1 Port"; got != want {
		t.Errorf("groups:\n%s\nwant:\n%s", got, want)
	}
}

func TestStructsWithThreeFieldsMustMatchScalarAndStringTypes(t *testing.T) {
	structs := []unit.Struct{
		// {id, name, count} is common: with other types it is a coincidence
		st("inference.rs", "ModelFacet", 1, "id:String", "name:String", "count:i64"),
		st("grants.rs", "Choice", 1, "id:String", "name:String", "count:usize"),
		// a view: a timestamp held as text
		st("events.rs", "CheckView", 1, "account_id:String", "state:String", "checked_through:String"),
		st("models.rs", "EventCheck", 1, "account_id:String", "state:String", "checked_through:jiff::Timestamp"),
		st("fleet.rs", "Space", 1, "total:u64", "reserved:u64", "free:u64"),
		st("protocol.rs", "Space", 1, "total:u64", "reserved:u64", "free:u64"),
		// one shape around different payloads: a generic Receipt<T>
		st("plan.rs", "ReceiptRecord", 1, "operation:String", "operation_id:uuid::Uuid", "result:HostingSession"),
		st("lot_freeze.rs", "Receipt", 1, "operation:String", "operation_id:uuid::Uuid", "result:LotFreezeResult"),
	}
	want := "fleet.rs:1 Space | protocol.rs:1 Space\nlot_freeze.rs:1 Receipt | plan.rs:1 ReceiptRecord"
	if got := structGroupStrings(Structs(structs, 3)); got != want {
		t.Errorf("groups:\n%s\nwant:\n%s", got, want)
	}
}
