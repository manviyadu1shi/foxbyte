// SPDX-License-Identifier: AGPL-3.0-or-later

package resultfmt

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"io"
	"os"
	"strings"
	"testing"
)

// The worksheet the console's Export menu writes too (web/src/xlsx.ts).
func TestSheetXMLSharedVectors(t *testing.T) {
	b, err := os.ReadFile("../../web/src/xlsx.vectors.json")
	if err != nil {
		t.Fatal(err)
	}
	var vs []struct {
		Name    string   `json:"name"`
		Columns []string `json:"columns"`
		Rows    [][]any  `json:"rows"`
		Sheet   string   `json:"sheet"`
	}
	if err := json.Unmarshal(b, &vs); err != nil {
		t.Fatal(err)
	}
	for _, v := range vs {
		if got := SheetXML(v.Columns, v.Rows); got != v.Sheet {
			t.Errorf("%s\n got %s\nwant %s", v.Name, got, v.Sheet)
		}
	}
}

// A workbook is a zip of the five parts Excel needs, and the same result makes
// the same file.
func TestXLSX(t *testing.T) {
	data, err := XLSX([]string{"id"}, [][]any{{int64(1)}})
	if err != nil {
		t.Fatal(err)
	}
	z, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, f := range z.File {
		names = append(names, f.Name)
		if f.Name == "xl/worksheets/sheet1.xml" {
			r, _ := f.Open()
			body, _ := io.ReadAll(r)
			if !strings.Contains(string(body), `<c r="A2"><v>1</v></c>`) {
				t.Errorf("sheet: %s", body)
			}
		}
	}
	if strings.Join(names, ",") != "[Content_Types].xml,_rels/.rels,xl/workbook.xml,xl/_rels/workbook.xml.rels,xl/worksheets/sheet1.xml" {
		t.Errorf("parts: %v", names)
	}
	again, _ := XLSX([]string{"id"}, [][]any{{int64(1)}})
	if !bytes.Equal(data, again) {
		t.Error("the same result made two different files")
	}
}

func TestColNameAndLimits(t *testing.T) {
	for i, want := range map[int]string{0: "A", 25: "Z", 26: "AA", 27: "AB", 701: "ZZ", 702: "AAA"} {
		if got := colName(i); got != want {
			t.Errorf("colName(%d) = %s, want %s", i, got, want)
		}
	}
	long := strings.Repeat("é", maxCellText+10)
	if n := len([]rune(truncateRunes(long, maxCellText))); n != maxCellText {
		t.Errorf("cut to %d characters", n)
	}
}
