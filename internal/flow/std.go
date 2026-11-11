package flow

// stdMethods are method names of common standard-library types and traits
// (Iterator, Option, Result, Vec, slices, str, String, maps and sets,
// Clone, AsRef, conversions, comparisons, I/O, sync and time). A call
// x.name() with one of these names most likely calls the standard library
// even when the codebase happens to define a single method of that name,
// so such calls are not resolved by uniqueness. Calls on self still are.
var stdMethods = setOf(
	// conversion and borrowing
	"as_ref", "as_mut", "as_str", "as_bytes", "as_slice", "as_deref", "as_ptr",
	"borrow", "borrow_mut", "clone", "cloned", "copied", "to_owned", "to_string",
	"to_vec", "into", "try_into", "from", "try_from", "from_str", "parse",
	// collections
	"len", "is_empty", "push", "push_str", "pop", "insert", "remove", "get",
	"get_mut", "contains", "contains_key", "entry", "or_insert", "or_default",
	"extend", "extend_from_slice", "append", "clear", "drain", "retain",
	"truncate", "reserve", "capacity", "with_capacity", "sort", "sort_by",
	"sort_by_key", "sort_unstable", "dedup", "keys", "values", "values_mut",
	"first", "last", "split", "split_once", "trim", "starts_with", "ends_with",
	"replace", "chars", "bytes", "lines", "join", "concat", "to_lowercase",
	"to_uppercase",
	// iterators
	"iter", "iter_mut", "into_iter", "next", "peek", "nth", "map", "filter",
	"filter_map", "flat_map", "flatten", "fold", "for_each", "collect", "count",
	"sum", "product", "max", "min", "max_by_key", "min_by_key", "rev", "zip",
	"enumerate", "take", "skip", "chain", "any", "all", "find", "find_map",
	"position", "windows", "chunks",
	// Option and Result
	"map_err", "and_then", "or_else", "unwrap", "unwrap_or", "unwrap_or_else",
	"unwrap_or_default", "expect", "ok", "err", "ok_or", "ok_or_else", "is_some",
	"is_none", "is_ok", "is_err", "take_if", "filter_map_err",
	// comparison, hashing, formatting
	"eq", "ne", "cmp", "partial_cmp", "hash", "fmt", "default",
	// I/O, sync, channels, time
	"read", "write", "flush", "lock", "send", "recv", "kind", "poll", "elapsed",
	"duration_since", "as_secs", "as_secs_f64", "as_millis",
	// numbers
	"abs", "checked_add", "checked_sub", "checked_mul", "saturating_add",
	"saturating_sub", "floor", "ceil", "round",
)

func setOf(names ...string) map[string]bool {
	out := make(map[string]bool, len(names))
	for _, n := range names {
		out[n] = true
	}
	return out
}

// goStdMethods are method names of common Go standard-library types and
// interfaces (http.ResponseWriter, io, fmt.Stringer, error, context, sync,
// database/sql, time, strings.Builder, bytes.Buffer, encoding/json). Like
// stdMethods, calls by these names from Go code are not resolved by
// uniqueness: w.Header() is an http.ResponseWriter's.
var goStdMethods = setOf(
	// net/http
	"Header", "Write", "WriteHeader", "ServeHTTP", "Do", "Get", "Post", "Set", "Add", "Del",
	// io, fmt, errors
	"Read", "Close", "Seek", "ReadAll", "WriteString", "WriteByte", "WriteRune",
	"String", "Error", "Unwrap", "Is", "As",
	// context and sync
	"Done", "Err", "Value", "Deadline", "Lock", "Unlock", "RLock", "RUnlock",
	"Wait", "Load", "Store", "Range", "Swap", "CompareAndSwap",
	// database/sql
	"Query", "QueryRow", "QueryContext", "QueryRowContext", "Exec", "ExecContext",
	"Scan", "Next", "Commit", "Rollback", "Begin", "BeginTx", "Prepare",
	// time, strings, bytes, encoding
	"Unix", "Format", "Sub", "Before", "After", "Equal", "IsZero", "UTC",
	"Len", "Reset", "Grow", "Bytes", "Encode", "Decode", "MarshalJSON", "UnmarshalJSON",
)
