package aiassist

import (
	"archive/zip"
	"bytes"
	"encoding/csv"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"path"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Customers send the spec of a new menu or section as Excel or Word (24 Nov 2026).
// Gemini reads pictures, PDFs and text, not Office files, so CRN gives it their text:
// each sheet of a workbook as CSV, a document's paragraphs with its tables' rows as
// "cell | cell". Only the text: no formulas, formats or pictures inside.

const (
	xlsxType = "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"
	docxType = "application/vnd.openxmlformats-officedocument.wordprocessingml.document"

	officeTextMax  = 60000    // characters of one file's text that Gemini gets
	officePartRead = 32 << 20 // bytes read from one part of the zip, against zip bombs
)

// officeText is the text of an Office file the customer attached.
func officeText(typ string, data []byte) (string, error) {
	z, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return "", errors.New("not an office file")
	}
	var text string
	switch typ {
	case xlsxType:
		text, err = xlsxText(z)
	case docxType:
		text, err = docxText(z)
	default:
		return "", errors.New("not an office type")
	}
	if err != nil {
		return "", err
	}
	if utf8.RuneCountInString(text) > officeTextMax {
		text = string([]rune(text)[:officeTextMax]) + fmt.Sprintf("\n(ตัดเหลือ %d ตัวอักษรแรกของไฟล์)", officeTextMax)
	}
	return text, nil
}

func open(z *zip.Reader, name string) (io.ReadCloser, error) {
	for _, f := range z.File {
		if f.Name == name {
			rc, err := f.Open()
			if err != nil {
				return nil, err
			}
			return struct {
				io.Reader
				io.Closer
			}{io.LimitReader(rc, officePartRead), rc}, nil
		}
	}
	return nil, fmt.Errorf("no %s", name)
}

// xlsxText is each sheet, in the workbook's order, as "[sheet: name]" and CSV.
func xlsxText(z *zip.Reader) (string, error) {
	type sheet struct{ name, file string }
	var sheets []sheet
	rels := map[string]string{}
	if rc, err := open(z, "xl/_rels/workbook.xml.rels"); err == nil {
		var r struct {
			Rel []struct {
				ID     string `xml:"Id,attr"`
				Target string `xml:"Target,attr"`
			} `xml:"Relationship"`
		}
		_ = xml.NewDecoder(rc).Decode(&r)
		rc.Close()
		for _, x := range r.Rel {
			target := strings.TrimPrefix(x.Target, "/")
			if !strings.HasPrefix(target, "xl/") {
				target = path.Join("xl", target)
			}
			rels[x.ID] = target
		}
	}
	rc, err := open(z, "xl/workbook.xml")
	if err != nil {
		return "", errors.New("not a workbook")
	}
	var wb struct {
		Sheets []struct {
			Name string     `xml:"name,attr"`
			Attr []xml.Attr `xml:",any,attr"`
		} `xml:"sheets>sheet"`
	}
	err = xml.NewDecoder(rc).Decode(&wb)
	rc.Close()
	if err != nil {
		return "", errors.New("not a workbook")
	}
	for i, s := range wb.Sheets {
		file := fmt.Sprintf("xl/worksheets/sheet%d.xml", i+1)
		for _, a := range s.Attr {
			if a.Name.Local == "id" && rels[a.Value] != "" {
				file = rels[a.Value]
			}
		}
		sheets = append(sheets, sheet{s.Name, file})
	}

	var shared []string
	if rc, err := open(z, "xl/sharedStrings.xml"); err == nil {
		shared, err = sharedStrings(rc)
		rc.Close()
		if err != nil {
			return "", err
		}
	}

	var out strings.Builder
	for _, s := range sheets {
		rc, err := open(z, s.file)
		if err != nil {
			continue
		}
		rows, err := sheetRows(rc, shared)
		rc.Close()
		if err != nil {
			return "", err
		}
		fmt.Fprintf(&out, "[sheet: %s]\n", s.name)
		w := csv.NewWriter(&out)
		_ = w.WriteAll(rows)
		if out.Len() > officeTextMax*4 {
			break
		}
	}
	return out.String(), nil
}

// sharedStrings is the workbook's table of strings, rich text joined.
func sharedStrings(r io.Reader) ([]string, error) {
	var out []string
	var cur strings.Builder
	inT := false
	dec := xml.NewDecoder(r)
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			return out, nil
		}
		if err != nil {
			return nil, errors.New("bad shared strings")
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "si":
				cur.Reset()
			case "t":
				inT = true
			}
		case xml.EndElement:
			switch t.Name.Local {
			case "si":
				out = append(out, cur.String())
			case "t":
				inT = false
			}
		case xml.CharData:
			if inT {
				cur.Write(t)
			}
		}
	}
}

// sheetRows is a sheet's cells as rows, each cell in its own column (A, B, ...).
func sheetRows(r io.Reader, shared []string) ([][]string, error) {
	var rows [][]string
	var row []string
	var ref, typ string
	var val strings.Builder
	in := ""
	dec := xml.NewDecoder(r)
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			return rows, nil
		}
		if err != nil {
			return nil, errors.New("bad sheet")
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "row":
				row = nil
			case "c":
				ref, typ = "", ""
				val.Reset()
				for _, a := range t.Attr {
					switch a.Name.Local {
					case "r":
						ref = a.Value
					case "t":
						typ = a.Value
					}
				}
			case "v", "t":
				in = t.Name.Local
			}
		case xml.EndElement:
			switch t.Name.Local {
			case "v", "t":
				in = ""
			case "c":
				v := val.String()
				switch typ {
				case "s":
					if i, err := strconv.Atoi(strings.TrimSpace(v)); err == nil && i >= 0 && i < len(shared) {
						v = shared[i]
					}
				case "b":
					v = map[string]string{"1": "TRUE", "0": "FALSE"}[v]
				}
				col := column(ref)
				if col < 0 {
					col = len(row)
				}
				if col > 16383 {
					continue
				}
				for len(row) < col {
					row = append(row, "")
				}
				row = append(row, v)
			case "row":
				rows = append(rows, row)
			}
		case xml.CharData:
			if in != "" {
				val.Write(t)
			}
		}
	}
}

// column is a cell reference's column from 0 (A1 → 0, D2 → 3), or -1.
func column(ref string) int {
	n := 0
	i := 0
	for ; i < len(ref) && ref[i] >= 'A' && ref[i] <= 'Z'; i++ {
		n = n*26 + int(ref[i]-'A'+1)
	}
	if i == 0 {
		return -1
	}
	return n - 1
}

// docxText is a document's paragraphs, one per line, and its tables' rows as
// "cell | cell".
func docxText(z *zip.Reader) (string, error) {
	rc, err := open(z, "word/document.xml")
	if err != nil {
		return "", errors.New("not a document")
	}
	defer rc.Close()
	var out strings.Builder
	var cell []string // the current row's cells, inside a table
	var cur strings.Builder
	cells, inT := 0, false
	dec := xml.NewDecoder(rc)
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", errors.New("bad document")
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "t":
				inT = true
			case "tab":
				cur.WriteString("\t")
			case "br":
				cur.WriteString(" ")
			case "tc":
				cells++
			}
		case xml.EndElement:
			switch t.Name.Local {
			case "t":
				inT = false
			case "p":
				if cells > 0 {
					cur.WriteString(" ")
				} else {
					out.WriteString(strings.TrimRight(cur.String(), " ") + "\n")
					cur.Reset()
				}
			case "tc":
				cells--
				cell = append(cell, strings.TrimSpace(cur.String()))
				cur.Reset()
			case "tr":
				out.WriteString(strings.Join(cell, " | ") + "\n")
				cell = nil
			}
		case xml.CharData:
			if inT {
				cur.Write(t)
			}
		}
		if out.Len() > officeTextMax*4 {
			break
		}
	}
	return out.String(), nil
}
