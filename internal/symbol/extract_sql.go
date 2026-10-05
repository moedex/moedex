package symbol

import (
	"regexp"
	"strings"
)

// SQLExtractor extracts SQL (MySQL/MariaDB) top-level definitions with a
// dependency-free regex/byte scan (no SQL parser, no cgo, no third-party). Like
// the C#/TS/CF extractors it favors precision over completeness and never errors.
//
// It covers the CREATE forms that name a reusable, file-level object in the
// configured mysql-scripts corpus:
//
//   - CREATE [DEFINER=..] PROCEDURE|FUNCTION name (..)  -> Func
//   - CREATE [..] TABLE|VIEW|TRIGGER|EVENT name         -> Type
//   - CREATE DATABASE|SCHEMA name                       -> Type
//
// Object names may be backtick-quoted, optionally db-qualified, and the leading
// modifier soup (OR REPLACE, TEMPORARY, DEFINER=.., ALGORITHM=.., SQL SECURITY ..,
// AGGREGATE) may appear in any order — the modifier group absorbs it. The name is
// captured WITHOUT its backticks, so tokenindex.Tokenize splits a snake_case name
// like `util_replication_show_slave_status` into {util,replication,show,slave,
// status} for the symbol-name ranking arm.
//
// The SQL-specific hazard, mirroring the JavaScript-in-.cfm trap the CF extractor
// guards against: these scripts BUILD DDL as strings — `SELECT CONCAT('CREATE
// TABLE ', p_table_name, ..)` and `CONCAT('CREATE TRIGGER trg_', ..)` — and they
// create transient locals inside procedure bodies — `    CREATE TEMPORARY TABLE
// day_num`. Neither is a file-level definition. Both are distinguished by
// position: every real top-level definition starts at COLUMN 0, while dynamic-SQL
// fragments sit inside a `'..'`/`CONCAT(..)`/`/*! .. */` and in-body temp tables
// are indented. So the match is anchored at column 0 (not the `^[ \t]*` the brace
// languages use, where members are legitimately indented inside a type). This
// also excludes `-- ` / `# ` / `/*! ` comment-led CREATEs for free.
//
// Known limitation accepted for precision: a version-gated `/*!50001 CREATE VIEW
// ..*/` (MariaDB dump form) is indented past column 0 by the comment prefix and is
// therefore skipped.
type SQLExtractor struct{}

// sqlBacktick is MySQL's identifier quote. It is concatenated into the pattern
// because a backtick cannot appear inside a Go raw-string (backtick-delimited)
// literal, which the other extractors use for their regexes.
const sqlBacktick = "`"

// sqlCreateRe matches one top-level CREATE statement and captures (1) the object
// keyword and (2) the unquoted object name. Anchored at column 0 (see the type
// doc for why). The modifier group is zero-or-more so a bare `CREATE TABLE x`
// matches as readily as `CREATE OR REPLACE DEFINER=..@.. PROCEDURE x`.
var sqlCreateRe = regexp.MustCompile(
	`(?im)^CREATE\s+` +
		`(?:(?:OR\s+REPLACE|TEMPORARY|AGGREGATE|DEFINER\s*=\s*\S+|ALGORITHM\s*=\s*\S+|SQL\s+SECURITY\s+\w+)\s+)*` +
		`(PROCEDURE|FUNCTION|TABLE|VIEW|TRIGGER|EVENT|DATABASE|SCHEMA)\s+` +
		`(?:IF\s+NOT\s+EXISTS\s+)?` +
		`(?:` + sqlBacktick + `?[A-Za-z_][A-Za-z0-9_$]*` + sqlBacktick + `?\s*\.\s*)?` + // optional db. qualifier
		sqlBacktick + `?([A-Za-z_][A-Za-z0-9_$]*)` + sqlBacktick + `?`,
)

// Extract scans SQL content for top-level CREATE definitions. Always returns a
// nil error.
//
// Body ranges: FindAllSubmatchIndex yields matches left-to-right, so each
// definition's body runs from its own CREATE to the start of the next top-level
// CREATE (or end of content for the last). That keeps the ranges non-overlapping
// and guarantees BodyStart <= NameStart < NameEnd <= BodyEnd, which is all the
// contextwin Enclosing consumer needs; SQL's BEGIN..END nesting and custom
// DELIMITERs make an exact body end far more trouble than it is worth here, and
// the ranking arm uses only the name.
func (SQLExtractor) Extract(content []byte) ([]Symbol, error) {
	ms := sqlCreateRe.FindAllSubmatchIndex(content, -1)
	if len(ms) == 0 {
		return nil, nil
	}
	syms := make([]Symbol, 0, len(ms))
	for i, m := range ms {
		kwStart, kwEnd := m[2], m[3]
		ns, ne := m[4], m[5]
		if ns < 0 || ne < 0 || kwStart < 0 {
			continue
		}
		bodyEnd := len(content)
		if i+1 < len(ms) {
			bodyEnd = ms[i+1][0]
		}
		syms = append(syms, Symbol{
			Name:      string(content[ns:ne]),
			Kind:      sqlKind(string(content[kwStart:kwEnd])),
			NameStart: ns,
			NameEnd:   ne,
			BodyStart: m[0],
			BodyEnd:   bodyEnd,
		})
	}
	return syms, nil
}

// sqlKind maps a CREATE object keyword to a symbol Kind. Routines (PROCEDURE,
// FUNCTION) are Func; every other named object (TABLE, VIEW, TRIGGER, EVENT,
// DATABASE, SCHEMA) is bucketed as Type — the ranking arm keys on the Name, so
// the precise Kind matters only as metadata for the context consumer.
func sqlKind(keyword string) Kind {
	switch strings.ToUpper(keyword) {
	case "PROCEDURE", "FUNCTION":
		return Func
	default:
		return Type
	}
}
