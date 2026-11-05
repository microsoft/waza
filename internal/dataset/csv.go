package dataset

import (
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/microsoft/waza/internal/rootedfile"
)

// Row represents a single CSV row with column name to value mapping.
type Row map[string]string

// LoadCSV reads a CSV file and returns rows as maps of column to value.
// The first row is treated as headers (column names).
func LoadCSV(path string) (rows []Row, err error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("csv: open %s: %w", path, err)
	}
	defer func() { err = errors.Join(err, f.Close()) }()

	return readCSV(f, path)
}

func readCSVAt(root *os.Root, path string) (rows []Row, err error) {
	f, err := rootedfile.Open(root, path)
	if err != nil {
		return nil, fmt.Errorf("csv: open confined %s: %w", path, err)
	}
	defer func() { err = errors.Join(err, f.Close()) }()
	info, err := f.Stat()
	if err != nil {
		return nil, fmt.Errorf("csv: inspecting %s: %w", path, err)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("csv: %s must be a regular file", path)
	}
	return readCSV(f, path)
}

func readCSV(input io.Reader, path string) ([]Row, error) {
	reader := csv.NewReader(input)
	records, err := reader.ReadAll()
	if err != nil {
		return nil, fmt.Errorf("csv: parse %s: %w", path, err)
	}

	if len(records) == 0 {
		return nil, fmt.Errorf("csv: %s is empty (no header row)", path)
	}

	headers := records[0]
	rows := make([]Row, 0, len(records)-1)

	for i, record := range records[1:] {
		if len(record) != len(headers) {
			return nil, fmt.Errorf("csv: row %d has %d columns, expected %d", i+2, len(record), len(headers))
		}
		row := make(Row, len(headers))
		for j, h := range headers {
			row[h] = record[j]
		}
		rows = append(rows, row)
	}

	return rows, nil
}

// LoadCSVRange reads rows in the given range [start, end] (1-based, inclusive).
// Row 1 is the first data row (after headers).
func LoadCSVRange(path string, start, end int) ([]Row, error) {
	if start < 1 {
		return nil, fmt.Errorf("csv: range start must be >= 1, got %d", start)
	}
	if end < start {
		return nil, fmt.Errorf("csv: range end (%d) must be >= start (%d)", end, start)
	}

	allRows, err := LoadCSV(path)
	if err != nil {
		return nil, err
	}

	return selectCSVRange(allRows, start, end), nil
}

func selectCSVRange(rows []Row, start, end int) []Row {
	return rows[min(start-1, len(rows)):min(end, len(rows))]
}
