// SPDX-License-Identifier: AGPL-3.0-or-later

package resultfmt

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

type vector struct {
	Name    string   `json:"name"`
	Columns []string `json:"columns"`
	Rows    [][]any  `json:"rows"`
	Format  string   `json:"format"`
	Header  bool     `json:"header"`
	Safe    bool     `json:"safe"`
	Table   string   `json:"table"`
	Want    string   `json:"want"`
}

// The cases the console's copy (web/src/copyformat.ts) is held to as well: the
// CLI and the console write a result the same way, or one of these fails.
func TestSharedVectors(t *testing.T) {
	b, err := os.ReadFile("../../web/src/copyformat.vectors.json")
	if err != nil {
		t.Fatal(err)
	}
	var vs []vector
	if err := json.Unmarshal(b, &vs); err != nil {
		t.Fatal(err)
	}
	if len(vs) < 10 {
		t.Fatalf("only %d cases", len(vs))
	}
	for _, v := range vs {
		got, err := Format(v.Columns, v.Rows, Options{Format: v.Format, Header: v.Header, Safe: v.Safe, Table: v.Table})
		if err != nil {
			t.Errorf("%s: %v", v.Name, err)
			continue
		}
		if got != v.Want {
			t.Errorf("%s\n got %q\nwant %q", v.Name, got, v.Want)
		}
	}
}

// The engine's own value types, which JSON never produces, write as the JSON
// ones do.
func TestEngineTypes(t *testing.T) {
	got, err := Format([]string{"a", "b", "c"}, [][]any{{int64(9007199254740993), int32(7), float32(1.5)}}, Options{Format: "csv"})
	if err != nil || got != "9007199254740993,7,1.5\n" {
		t.Errorf("%q %v", got, err)
	}
	if s, _ := Format([]string{"n"}, [][]any{{json.Number("12.50")}}, Options{Format: "json"}); !strings.Contains(s, `"n": 12.50`) {
		t.Errorf("json.Number: %q", s)
	}
}

func TestFormatRefusals(t *testing.T) {
	if _, err := Format(nil, nil, Options{Format: "xml"}); err == nil {
		t.Error("an unknown format was accepted")
	}
	if _, err := Format([]string{"a"}, [][]any{{1}}, Options{Format: "insert"}); err == nil {
		t.Error("INSERT statements without a table name")
	}
}

func TestIdent(t *testing.T) {
	for in, want := range map[string]string{"id": "id", "user": `"user"`, "Name": `"Name"`, "a b": `"a b"`, `q"x`: `"q""x"`, "_t1$": "_t1$"} {
		if got := Ident(in); got != want {
			t.Errorf("Ident(%q) = %s, want %s", in, got, want)
		}
	}
}
