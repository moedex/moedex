package symbol

import "testing"

func TestSQLExtractor_RoutinesTablesEvents(t *testing.T) {
	// Shapes drawn from the real mysql-scripts corpus: a DEFINER-qualified
	// procedure, a backticked table, a lowercase database, and a DEFINER-qualified
	// event — all at column 0.
	src := []byte("create database if not exists mysql_heartbeat ;\n" +
		"\n" +
		"CREATE TABLE `mysql_heartbeat` (\n" +
		"  server_name varchar(100) NOT NULL\n" +
		") ENGINE=InnoDB;\n" +
		"\n" +
		"CREATE DEFINER=`admin`@`%` PROCEDURE `util_replication_show_slave_status`(IN `master_name` varchar(100))\n" +
		"BEGIN\n" +
		"  SHOW SLAVE STATUS;\n" +
		"END\n" +
		"\n" +
		"CREATE DEFINER=`admin`@`%` EVENT `update_mysql_heartbeat_event` ON SCHEDULE every 5 SECOND DO\n" +
		"  insert into mysql_heartbeat values (now());\n")

	syms, err := SQLExtractor{}.Extract(src)
	if err != nil {
		t.Fatalf("Extract returned error: %v", err)
	}
	by := symNames(syms)

	wantFunc := []string{"util_replication_show_slave_status"}
	for _, n := range wantFunc {
		s, ok := by[n]
		if !ok {
			t.Errorf("missing Func symbol %q; got %v", n, names(syms))
			continue
		}
		if s.Kind != Func {
			t.Errorf("%q: kind = %v, want Func", n, s.Kind)
		}
		if got := string(src[s.NameStart:s.NameEnd]); got != n {
			t.Errorf("%q: content[NameStart:NameEnd] = %q, want %q (backticks must be excluded)", n, got, n)
		}
	}

	wantType := []string{"mysql_heartbeat", "update_mysql_heartbeat_event"}
	for _, n := range wantType {
		s, ok := by[n]
		if !ok {
			t.Errorf("missing Type symbol %q; got %v", n, names(syms))
			continue
		}
		if s.Kind != Type {
			t.Errorf("%q: kind = %v, want Type", n, s.Kind)
		}
		if got := string(src[s.NameStart:s.NameEnd]); got != n {
			t.Errorf("%q: content[NameStart:NameEnd] = %q, want %q", n, got, n)
		}
	}

	// "mysql_heartbeat" is defined twice (database + table) — both should appear,
	// reinforcing the name. Ranking tolerates duplicate-name symbols.
	count := 0
	for _, s := range syms {
		if s.Name == "mysql_heartbeat" {
			count++
		}
	}
	if count != 2 {
		t.Errorf("mysql_heartbeat defined as both database and table: got %d symbols, want 2", count)
	}
}

// TestSQLExtractor_SkipsDynamicSQL is the SQL analog of the CF JS-trap test:
// CREATE statements assembled as strings inside CONCAT(..) are dynamic SQL, not
// definitions, and must NOT be extracted. The procedure that builds them IS.
func TestSQLExtractor_SkipsDynamicSQL(t *testing.T) {
	src := []byte("CREATE DEFINER=`admin`@`%` PROCEDURE `util_create_history_scripts`(IN `p_table_name` varchar(200))\n" +
		"BEGIN\n" +
		"  SELECT CONCAT('CREATE TABLE ', p_table_name,'_history (', '...');\n" +
		"  SELECT CONCAT('CREATE TRIGGER trg_', p_table_name,'_history_insert ');\n" +
		"END\n")

	syms, _ := SQLExtractor{}.Extract(src)
	by := symNames(syms)

	if _, ok := by["util_create_history_scripts"]; !ok {
		t.Errorf("missing the real procedure util_create_history_scripts; got %v", names(syms))
	}
	for _, bad := range []string{"trg_", "_history"} {
		if _, ok := by[bad]; ok {
			t.Errorf("dynamic-SQL fragment %q was extracted as a symbol; got %v", bad, names(syms))
		}
	}
	// Exactly one symbol — only the procedure.
	if len(syms) != 1 {
		t.Errorf("expected only the procedure symbol, got %d: %v", len(syms), names(syms))
	}
}

// TestSQLExtractor_SkipsInBodyTempTables: indented `CREATE TEMPORARY TABLE`
// inside a procedure body is a transient local, not a file-level definition.
// Column-0 anchoring excludes it.
func TestSQLExtractor_SkipsInBodyTempTables(t *testing.T) {
	src := []byte("CREATE DEFINER=`admin`@`%` PROCEDURE `date_partition_maintenance`(IN `p_schema` varchar(255))\n" +
		"BEGIN\n" +
		"    CREATE TEMPORARY TABLE day_num AS SELECT 1;\n" +
		"    CREATE TEMPORARY TABLE partition_date (d date);\n" +
		"END\n")

	syms, _ := SQLExtractor{}.Extract(src)
	by := symNames(syms)

	if _, ok := by["date_partition_maintenance"]; !ok {
		t.Errorf("missing the procedure date_partition_maintenance; got %v", names(syms))
	}
	for _, local := range []string{"day_num", "partition_date"} {
		if _, ok := by[local]; ok {
			t.Errorf("in-body temp table %q was extracted; column-0 anchoring should skip it; got %v", local, names(syms))
		}
	}
	if len(syms) != 1 {
		t.Errorf("expected only the procedure, got %d: %v", len(syms), names(syms))
	}
}

func TestSQLExtractor_ModifierAndQuotingVariants(t *testing.T) {
	cases := []struct {
		name string
		src  string
		want string
		kind Kind
	}{
		{"bare table", "CREATE TABLE plain_table (id int);\n", "plain_table", Type},
		{"if not exists", "CREATE TABLE IF NOT EXISTS guarded (id int);\n", "guarded", Type},
		{"lowercase function", "create function add_two(a int) returns int return a+2;\n", "add_two", Func},
		{"or replace view", "CREATE OR REPLACE VIEW active_users AS SELECT 1;\n", "active_users", Type},
		{"algorithm+definer view", "CREATE ALGORITHM=UNDEFINED DEFINER=`admin`@`%` SQL SECURITY DEFINER VIEW v_report AS SELECT 1;\n", "v_report", Type},
		{"db-qualified proc", "CREATE PROCEDURE `mydb`.`do_work`() BEGIN END\n", "do_work", Func},
		{"schema keyword", "CREATE SCHEMA reporting;\n", "reporting", Type},
		{"trigger", "CREATE TRIGGER trg_audit BEFORE INSERT ON t FOR EACH ROW SET @x=1;\n", "trg_audit", Type},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			syms, _ := SQLExtractor{}.Extract([]byte(c.src))
			by := symNames(syms)
			s, ok := by[c.want]
			if !ok {
				t.Fatalf("missing %q; got %v", c.want, names(syms))
			}
			if s.Kind != c.kind {
				t.Errorf("%q: kind = %v, want %v", c.want, s.Kind, c.kind)
			}
			if got := string([]byte(c.src)[s.NameStart:s.NameEnd]); got != c.want {
				t.Errorf("%q: name offsets slice %q, want %q", c.want, got, c.want)
			}
		})
	}
}

func TestSQLExtractor_BodyRangesValidAndEnclosing(t *testing.T) {
	src := []byte("CREATE TABLE first (id int);\n" +
		"CREATE PROCEDURE second()\n" +
		"BEGIN\n" +
		"  SELECT marker;\n" +
		"END\n")

	syms, _ := SQLExtractor{}.Extract(src)
	for _, s := range syms {
		if s.BodyStart < 0 || s.BodyEnd > len(src) || s.BodyStart >= s.BodyEnd {
			t.Errorf("%q: invalid body range [%d,%d) (len %d)", s.Name, s.BodyStart, s.BodyEnd, len(src))
		}
		if !(s.BodyStart <= s.NameStart && s.NameEnd <= s.BodyEnd) {
			t.Errorf("%q: name [%d,%d) not within body [%d,%d)", s.Name, s.NameStart, s.NameEnd, s.BodyStart, s.BodyEnd)
		}
	}

	ix := NewIndex()
	ix.Set(0, syms)
	// An offset inside the procedure body resolves to `second`, not `first`.
	off := indexOf(src, "marker")
	s, ok := ix.Enclosing(0, off)
	if !ok {
		t.Fatalf("Enclosing found nothing at offset %d", off)
	}
	if s.Name != "second" {
		t.Errorf("Enclosing = %q, want second", s.Name)
	}
}

func TestSQLExtractor_NeverPanicsOnGarbage(t *testing.T) {
	inputs := [][]byte{
		nil,
		[]byte(""),
		[]byte("CREATE"),
		[]byte("CREATE PROCEDURE"),
		[]byte("CREATE TABLE `unterminated"),
		[]byte("-- CREATE TABLE commented_out (id int);"),
		[]byte("not sql at all, just prose with create table in the middle"),
		[]byte("CREATE TABLE \xff\xfe garbage bytes"),
	}
	for i, in := range inputs {
		syms, err := SQLExtractor{}.Extract(in)
		if err != nil {
			t.Errorf("input %d: unexpected error %v", i, err)
		}
		for _, s := range syms {
			if s.BodyStart < 0 || s.BodyEnd > len(in) || s.NameStart < 0 || s.NameEnd > len(in) {
				t.Errorf("input %d: out-of-bounds offsets in %+v (len %d)", i, s, len(in))
			}
		}
	}
	// A leading-whitespace comment must not yield a symbol.
	commentSyms, _ := SQLExtractor{}.Extract([]byte("-- CREATE TABLE commented_out (id int);\n"))
	if len(commentSyms) != 0 {
		t.Errorf("comment-led CREATE produced symbols: %v", names(commentSyms))
	}
}
