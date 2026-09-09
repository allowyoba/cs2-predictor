package valvevrs

import (
	"fmt"
	"strconv"
	"strings"
)

// row is one parsed markdown-table row, before identity resolution.
type row struct {
	standing int
	points   int
	name     string
	roster   []string
}

// parseTable parses a Valve Regional Standings markdown file's pipe table.
// The published format is a title line, a blank line, a header row, a
// ":-"-style alignment separator row, then one data row per team:
// "| Standing | Points | Team Name | Roster | details-link |". Extra or
// missing trailing columns are tolerated; only the first four are used.
// Non-data lines (title, header, separator, anything malformed) are
// skipped rather than treated as an error — a cosmetic change to a line
// this parser doesn't care about must not break ranking sync.
func parseTable(content string) ([]row, error) {
	var rows []row
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "|") {
			continue
		}
		cells := splitCells(line)
		if len(cells) < 3 {
			continue
		}
		standing, err := strconv.Atoi(cells[0])
		if err != nil {
			continue // header ("Standing") / separator (":-") / malformed — not a data row
		}
		points, err := strconv.Atoi(cells[1])
		if err != nil {
			continue
		}
		name := cells[2]
		if name == "" {
			continue
		}
		var roster []string
		if len(cells) > 3 {
			roster = splitRoster(cells[3])
		}
		rows = append(rows, row{standing: standing, points: points, name: name, roster: roster})
	}
	if len(rows) == 0 {
		return nil, fmt.Errorf("valve vrs: no standings rows found in table")
	}
	return rows, nil
}

func splitCells(line string) []string {
	line = strings.Trim(line, "|")
	parts := strings.Split(line, "|")
	cells := make([]string, len(parts))
	for i, p := range parts {
		cells[i] = strings.TrimSpace(p)
	}
	return cells
}

func splitRoster(cell string) []string {
	if cell == "" {
		return nil
	}
	parts := strings.Split(cell, ",")
	roster := make([]string, 0, len(parts))
	for _, p := range parts {
		if name := strings.TrimSpace(p); name != "" {
			roster = append(roster, name)
		}
	}
	return roster
}
