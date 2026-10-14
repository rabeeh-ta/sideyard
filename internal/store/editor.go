package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// The table editor and SQL editor work on an app's database file. Under
// the systemd runner they run inside `sideyard sqlite-query` as the
// project's user; the platform talks to it with HelperRequest/Response.

const (
	maxQueryRows = 1000
	maxPageRows  = 500
	maxCellChars = 10_000
	rowidColumn  = "__sideyard_rowid"
)

type Column struct {
	Name    string  `json:"name"`
	Type    string  `json:"type"`
	NotNull bool    `json:"notNull"`
	Default *string `json:"default"` // the SQL expression, if any
	// PK is the column's position in the primary key, 0 if not part of it.
	PK        int  `json:"pk"`
	Generated bool `json:"generated"`
}

type Index struct {
	Name    string   `json:"name"`
	Unique  bool     `json:"unique"`
	Columns []string `json:"columns"`
}

type TableInfo struct {
	Name string `json:"name"`
	// Type is table, view or virtual. Only tables are editable.
	Type         string   `json:"type"`
	Rows         int64    `json:"rows"`
	WithoutRowid bool     `json:"withoutRowid"`
	Columns      []Column `json:"columns"`
	Indexes      []Index  `json:"indexes"`
	SQL          string   `json:"sql"`
}

func quoteIdent(s string) string {
	return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
}

// openFile opens an app's database. Without write, query_only makes SQLite
// refuse anything that would change the file, whatever the SQL says.
func openFile(path string, write bool) (*sql.DB, error) {
	dsn := "file:" + path + "?_pragma=busy_timeout(5000)"
	if !write {
		dsn += "&_pragma=query_only(1)"
	}
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1) // changes() and last_insert_rowid() are per connection
	return db, nil
}

// --- schema ---

func schema(ctx context.Context, db *sql.DB) ([]TableInfo, error) {
	rows, err := db.QueryContext(ctx, `SELECT l.name, l.type, l.wr, COALESCE(s.sql, '')
		FROM pragma_table_list AS l LEFT JOIN sqlite_schema AS s ON s.name = l.name
		WHERE l.schema = 'main' AND l.type IN ('table', 'view', 'virtual') AND l.name NOT LIKE 'sqlite_%'
		ORDER BY l.name`)
	if err != nil {
		return nil, err
	}
	var tables []TableInfo
	for rows.Next() {
		var t TableInfo
		if err := rows.Scan(&t.Name, &t.Type, &t.WithoutRowid, &t.SQL); err != nil {
			rows.Close()
			return nil, err
		}
		tables = append(tables, t)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range tables {
		t := &tables[i]
		if t.Columns, err = columns(ctx, db, t.Name); err != nil {
			return nil, err
		}
		if t.Indexes, err = indexes(ctx, db, t.Name); err != nil {
			return nil, err
		}
		if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM `+quoteIdent(t.Name)).Scan(&t.Rows); err != nil {
			t.Rows = -1
		}
	}
	if tables == nil {
		tables = []TableInfo{}
	}
	return tables, nil
}

func columns(ctx context.Context, db *sql.DB, table string) ([]Column, error) {
	rows, err := db.QueryContext(ctx, `SELECT name, type, "notnull", dflt_value, pk, hidden FROM pragma_table_xinfo(?)`, table)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	cols := []Column{}
	for rows.Next() {
		var c Column
		var dflt sql.NullString
		var hidden int
		if err := rows.Scan(&c.Name, &c.Type, &c.NotNull, &dflt, &c.PK, &hidden); err != nil {
			return nil, err
		}
		if hidden == 1 {
			continue // a virtual table's hidden column
		}
		c.Generated = hidden == 2 || hidden == 3
		if dflt.Valid {
			c.Default = &dflt.String
		}
		cols = append(cols, c)
	}
	return cols, rows.Err()
}

func indexes(ctx context.Context, db *sql.DB, table string) ([]Index, error) {
	rows, err := db.QueryContext(ctx, `SELECT name, "unique" FROM pragma_index_list(?)`, table)
	if err != nil {
		return nil, err
	}
	var out []Index
	for rows.Next() {
		var ix Index
		if err := rows.Scan(&ix.Name, &ix.Unique); err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, ix)
	}
	rows.Close()
	for i := range out {
		cols, err := db.QueryContext(ctx, `SELECT COALESCE(name, '<expression>') FROM pragma_index_info(?) ORDER BY seqno`, out[i].Name)
		if err != nil {
			return nil, err
		}
		for cols.Next() {
			var c string
			cols.Scan(&c)
			out[i].Columns = append(out[i].Columns, c)
		}
		cols.Close()
	}
	if out == nil {
		out = []Index{}
	}
	return out, nil
}

func findTable(ctx context.Context, db *sql.DB, name string) (TableInfo, error) {
	tables, err := schema(ctx, db)
	if err != nil {
		return TableInfo{}, err
	}
	for _, t := range tables {
		if t.Name == name {
			return t, nil
		}
	}
	return TableInfo{}, fmt.Errorf("no table %q", name)
}

func (t TableInfo) column(name string) (Column, bool) {
	i := slices.IndexFunc(t.Columns, func(c Column) bool { return c.Name == name })
	if i < 0 {
		return Column{}, false
	}
	return t.Columns[i], true
}

// pkColumns returns the primary key's columns in key order.
func (t TableInfo) pkColumns() []string {
	var pk []Column
	for _, c := range t.Columns {
		if c.PK > 0 {
			pk = append(pk, c)
		}
	}
	slices.SortFunc(pk, func(a, b Column) int { return a.PK - b.PK })
	names := make([]string, len(pk))
	for i, c := range pk {
		names[i] = c.Name
	}
	return names
}

// --- rows ---

type Sort struct {
	Column string `json:"column"`
	Desc   bool   `json:"desc"`
}

type Filter struct {
	Column string `json:"column"`
	// Op is eq, neq, gt, gte, lt, lte, contains, is_null or not_null.
	Op    string `json:"op"`
	Value string `json:"value"`
}

type RowsRequest struct {
	Table   string   `json:"table"`
	Limit   int      `json:"limit"`
	Offset  int      `json:"offset"`
	Sort    []Sort   `json:"sort"`
	Filters []Filter `json:"filters"`
}

// RowKey identifies one row: by rowid, or by primary key for a WITHOUT
// ROWID table.
type RowKey struct {
	Rowid *int64         `json:"rowid,omitempty"`
	PK    map[string]any `json:"pk,omitempty"`
}

type RowsResult struct {
	Columns  []string `json:"columns"`
	Rows     [][]any  `json:"rows"`
	Keys     []RowKey `json:"keys"` // empty for views, which can't be edited
	Total    int64    `json:"total"`
	Editable bool     `json:"editable"`
}

var filterOps = map[string]string{
	"eq": "=", "neq": "!=", "gt": ">", "gte": ">=", "lt": "<", "lte": "<=",
	"contains": "LIKE", "is_null": "IS NULL", "not_null": "IS NOT NULL",
}

func readRows(ctx context.Context, db *sql.DB, req RowsRequest) (RowsResult, error) {
	t, err := findTable(ctx, db, req.Table)
	if err != nil {
		return RowsResult{}, err
	}
	if req.Limit <= 0 || req.Limit > maxPageRows {
		req.Limit = 100
	}
	var where []string
	var args []any
	for _, f := range req.Filters {
		if _, ok := t.column(f.Column); !ok {
			return RowsResult{}, fmt.Errorf("no column %q", f.Column)
		}
		op, ok := filterOps[f.Op]
		if !ok {
			return RowsResult{}, fmt.Errorf("unknown filter %q", f.Op)
		}
		switch f.Op {
		case "is_null", "not_null":
			where = append(where, quoteIdent(f.Column)+" "+op)
		case "contains":
			where = append(where, quoteIdent(f.Column)+" LIKE ? ESCAPE '\\'")
			args = append(args, "%"+likeEscaper.Replace(f.Value)+"%")
		default:
			where = append(where, quoteIdent(f.Column)+" "+op+" ?")
			args = append(args, f.Value)
		}
	}
	whereSQL := ""
	if len(where) > 0 {
		whereSQL = " WHERE " + strings.Join(where, " AND ")
	}

	res := RowsResult{Columns: []string{}, Rows: [][]any{}, Keys: []RowKey{}, Editable: t.Type == "table"}
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM `+quoteIdent(t.Name)+whereSQL, args...).Scan(&res.Total); err != nil {
		return res, err
	}

	byRowid := t.Type == "table" && !t.WithoutRowid
	// "+col" returns a column's stored value without its declared type, so
	// the driver doesn't turn DATETIME text into a time and back.
	sel := make([]string, 0, len(t.Columns)+1)
	if byRowid {
		sel = append(sel, "rowid AS "+quoteIdent(rowidColumn))
	}
	for _, c := range t.Columns {
		sel = append(sel, "+"+quoteIdent(c.Name)+" AS "+quoteIdent(c.Name))
		res.Columns = append(res.Columns, c.Name)
	}
	var order []string
	for _, s := range req.Sort {
		if _, ok := t.column(s.Column); !ok {
			return res, fmt.Errorf("no column %q", s.Column)
		}
		dir := "ASC"
		if s.Desc {
			dir = "DESC"
		}
		order = append(order, quoteIdent(s.Column)+" "+dir)
	}
	switch {
	case byRowid:
		order = append(order, "rowid")
	case t.Type == "table":
		for _, c := range t.pkColumns() {
			order = append(order, quoteIdent(c))
		}
	}
	q := `SELECT ` + strings.Join(sel, ", ") + ` FROM ` + quoteIdent(t.Name) + whereSQL
	if len(order) > 0 {
		q += ` ORDER BY ` + strings.Join(order, ", ")
	}
	q += ` LIMIT ? OFFSET ?`
	rows, err := db.QueryContext(ctx, q, append(args, req.Limit, req.Offset)...)
	if err != nil {
		return res, err
	}
	defer rows.Close()
	pk := t.pkColumns()
	for rows.Next() {
		vals := make([]any, len(sel))
		ptrs := make([]any, len(sel))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return res, err
		}
		if byRowid {
			id, _ := vals[0].(int64)
			res.Keys = append(res.Keys, RowKey{Rowid: &id})
			vals = vals[1:]
		} else if t.Type == "table" {
			key := RowKey{PK: map[string]any{}}
			for _, name := range pk {
				key.PK[name] = vals[slices.Index(res.Columns, name)]
			}
			res.Keys = append(res.Keys, key)
		}
		for i, v := range vals {
			vals[i] = cell(v)
		}
		res.Rows = append(res.Rows, vals)
	}
	return res, rows.Err()
}

var likeEscaper = strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)

// Blob is how a binary value reaches the dashboard: its size, not its bytes.
type Blob struct {
	Blob int `json:"$blob"`
}

// cell makes a value JSON-friendly: blobs become their size, integers too
// big for JavaScript become strings, and long text is cut short.
func cell(v any) any {
	switch x := v.(type) {
	case []byte:
		if !utf8.Valid(x) {
			return Blob{len(x)}
		}
		v = string(x)
	case int64:
		if x > 1<<53 || x < -(1<<53) {
			return strconv.FormatInt(x, 10)
		}
	case float64:
		if math.IsInf(x, 0) || math.IsNaN(x) {
			return strconv.FormatFloat(x, 'g', -1, 64)
		}
	case time.Time:
		return x.UTC().Format(time.RFC3339Nano)
	}
	if s, ok := v.(string); ok && len(s) > maxCellChars {
		return strings.ToValidUTF8(s[:maxCellChars], "") + "…"
	}
	return v
}

// --- edits ---

// Mutation inserts, updates or deletes rows. Values are bound as given:
// strings from the editor take the column's type by SQLite's affinity
// rules, and null is NULL.
type Mutation struct {
	Table  string         `json:"table"`
	Kind   string         `json:"kind"` // insert, update or delete
	Values map[string]any `json:"values"`
	Keys   []RowKey       `json:"keys"` // update: one; delete: one or more
}

type MutationResult struct {
	Affected int64 `json:"affected"`
	Rowid    int64 `json:"rowid,omitempty"` // of an inserted row
}

func (t TableInfo) keyWhere(k RowKey) (string, []any, error) {
	if k.Rowid != nil && !t.WithoutRowid {
		return "rowid = ?", []any{*k.Rowid}, nil
	}
	pk := t.pkColumns()
	if len(pk) == 0 || len(k.PK) != len(pk) {
		return "", nil, errors.New("row key doesn't match the table's primary key")
	}
	var conds []string
	var args []any
	for _, c := range pk {
		v, ok := k.PK[c]
		if !ok {
			return "", nil, fmt.Errorf("row key is missing %q", c)
		}
		conds = append(conds, quoteIdent(c)+" IS ?")
		args = append(args, bindValue(v))
	}
	return strings.Join(conds, " AND "), args, nil
}

// bindValue turns a JSON value into a SQL argument; json.Number keeps
// integers exact.
func bindValue(v any) any {
	switch x := v.(type) {
	case json.Number:
		if i, err := x.Int64(); err == nil {
			return i
		}
		f, _ := x.Float64()
		return f
	case bool:
		if x {
			return int64(1)
		}
		return int64(0)
	}
	return v
}

func mutate(ctx context.Context, db *sql.DB, m Mutation) (MutationResult, error) {
	t, err := findTable(ctx, db, m.Table)
	if err != nil {
		return MutationResult{}, err
	}
	if t.Type != "table" {
		return MutationResult{}, fmt.Errorf("%s is a %s and can't be edited", t.Name, t.Type)
	}
	var cols []string
	var args []any
	for name, v := range m.Values {
		c, ok := t.column(name)
		if !ok {
			return MutationResult{}, fmt.Errorf("no column %q", name)
		}
		if c.Generated {
			return MutationResult{}, fmt.Errorf("%s is a generated column", name)
		}
		cols = append(cols, name)
		args = append(args, bindValue(v))
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return MutationResult{}, err
	}
	defer tx.Rollback()
	var res MutationResult
	switch m.Kind {
	case "insert":
		q := `INSERT INTO ` + quoteIdent(t.Name) + ` DEFAULT VALUES`
		if len(cols) > 0 {
			quoted := make([]string, len(cols))
			for i, c := range cols {
				quoted[i] = quoteIdent(c)
			}
			q = `INSERT INTO ` + quoteIdent(t.Name) + ` (` + strings.Join(quoted, ", ") + `) VALUES (` +
				strings.TrimSuffix(strings.Repeat("?, ", len(cols)), ", ") + `)`
		}
		r, err := tx.ExecContext(ctx, q, args...)
		if err != nil {
			return res, err
		}
		res.Affected, _ = r.RowsAffected()
		res.Rowid, _ = r.LastInsertId()
	case "update":
		if len(m.Keys) != 1 || len(cols) == 0 {
			return res, errors.New("an update needs one row and at least one value")
		}
		set := make([]string, len(cols))
		for i, c := range cols {
			set[i] = quoteIdent(c) + " = ?"
		}
		where, keyArgs, err := t.keyWhere(m.Keys[0])
		if err != nil {
			return res, err
		}
		r, err := tx.ExecContext(ctx, `UPDATE `+quoteIdent(t.Name)+` SET `+strings.Join(set, ", ")+` WHERE `+where, append(args, keyArgs...)...)
		if err != nil {
			return res, err
		}
		if res.Affected, _ = r.RowsAffected(); res.Affected != 1 {
			return res, fmt.Errorf("the row wasn't found (changed %d rows); refresh and try again", res.Affected)
		}
	case "delete":
		if len(m.Keys) == 0 {
			return res, errors.New("no rows to delete")
		}
		for _, k := range m.Keys {
			where, keyArgs, err := t.keyWhere(k)
			if err != nil {
				return res, err
			}
			r, err := tx.ExecContext(ctx, `DELETE FROM `+quoteIdent(t.Name)+` WHERE `+where, keyArgs...)
			if err != nil {
				return res, err
			}
			n, _ := r.RowsAffected()
			res.Affected += n
		}
	default:
		return res, fmt.Errorf("unknown change %q", m.Kind)
	}
	return res, tx.Commit()
}

// --- structure ---

type ColumnDef struct {
	Name    string `json:"name"`
	Type    string `json:"type"`
	PK      bool   `json:"pk"`
	NotNull bool   `json:"notNull"`
	Unique  bool   `json:"unique"`
	// Default is a value: a number, NULL, CURRENT_TIMESTAMP and the like as
	// is, anything else as a string.
	Default *string `json:"default"`
}

// Alter changes a database's structure.
type Alter struct {
	Kind    string      `json:"kind"` // create_table, add_column, rename_column, drop_column, rename_table, drop_table
	Table   string      `json:"table"`
	Column  string      `json:"column"`
	NewName string      `json:"newName"`
	Columns []ColumnDef `json:"columns"` // create_table
	Def     *ColumnDef  `json:"def"`     // add_column
}

var (
	typeRe     = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9 _]*(\(\s*\d+\s*(,\s*\d+\s*)?\))?$`)
	numberRe   = regexp.MustCompile(`^[-+]?(\d+(\.\d*)?|\.\d+)([eE][-+]?\d+)?$`)
	defaultKws = []string{"NULL", "CURRENT_TIMESTAMP", "CURRENT_DATE", "CURRENT_TIME", "TRUE", "FALSE"}
)

func (d ColumnDef) sql(single bool) (string, error) {
	if strings.TrimSpace(d.Name) == "" {
		return "", errors.New("every column needs a name")
	}
	if d.Type != "" && !typeRe.MatchString(d.Type) {
		return "", fmt.Errorf("type %q isn't a type name like TEXT, INTEGER or VARCHAR(255)", d.Type)
	}
	s := quoteIdent(d.Name)
	if d.Type != "" {
		s += " " + d.Type
	}
	if d.PK && single {
		s += " PRIMARY KEY"
	}
	if d.NotNull {
		s += " NOT NULL"
	}
	if d.Unique {
		s += " UNIQUE"
	}
	if d.Default != nil {
		v := strings.TrimSpace(*d.Default)
		switch {
		case numberRe.MatchString(v), slices.Contains(defaultKws, strings.ToUpper(v)):
			s += " DEFAULT " + v
		default:
			s += " DEFAULT '" + strings.ReplaceAll(v, "'", "''") + "'"
		}
	}
	return s, nil
}

// AlterSQL is the statement an Alter runs, so the dashboard can show it.
func AlterSQL(a Alter) (string, error) {
	t := quoteIdent(a.Table)
	switch a.Kind {
	case "create_table":
		if strings.TrimSpace(a.Table) == "" || len(a.Columns) == 0 {
			return "", errors.New("a table needs a name and at least one column")
		}
		var pk []string
		for _, c := range a.Columns {
			if c.PK {
				pk = append(pk, quoteIdent(c.Name))
			}
		}
		defs := make([]string, 0, len(a.Columns)+1)
		for _, c := range a.Columns {
			d, err := c.sql(len(pk) == 1)
			if err != nil {
				return "", err
			}
			defs = append(defs, d)
		}
		if len(pk) > 1 {
			defs = append(defs, "PRIMARY KEY ("+strings.Join(pk, ", ")+")")
		}
		return "CREATE TABLE " + t + " (\n  " + strings.Join(defs, ",\n  ") + "\n)", nil
	case "add_column":
		if a.Def == nil {
			return "", errors.New("missing column")
		}
		if a.Def.PK || a.Def.Unique {
			return "", errors.New("SQLite can't add a primary key or unique column to an existing table")
		}
		d, err := a.Def.sql(false)
		if err != nil {
			return "", err
		}
		return "ALTER TABLE " + t + " ADD COLUMN " + d, nil
	case "rename_column":
		if strings.TrimSpace(a.NewName) == "" {
			return "", errors.New("missing the new name")
		}
		return "ALTER TABLE " + t + " RENAME COLUMN " + quoteIdent(a.Column) + " TO " + quoteIdent(a.NewName), nil
	case "drop_column":
		return "ALTER TABLE " + t + " DROP COLUMN " + quoteIdent(a.Column), nil
	case "rename_table":
		if strings.TrimSpace(a.NewName) == "" {
			return "", errors.New("missing the new name")
		}
		return "ALTER TABLE " + t + " RENAME TO " + quoteIdent(a.NewName), nil
	case "drop_table":
		return "DROP TABLE " + t, nil
	}
	return "", fmt.Errorf("unknown change %q", a.Kind)
}

// --- SQL editor ---

type ExecResult struct {
	QueryResult
	// Changes is how many rows the last INSERT, UPDATE or DELETE changed.
	Changes int64 `json:"changes"`
	// NeedsWrite means the SQL would change the database and was refused
	// because the request didn't allow writes. Nothing was changed.
	NeedsWrite bool `json:"needsWrite"`
}

// QueryResult is a statement's columns and rows, capped at maxQueryRows.
type QueryResult struct {
	Columns   []string `json:"columns"`
	Rows      [][]any  `json:"rows"`
	Truncated bool     `json:"truncated"`
}

// execSQL runs a script and returns the last statement's rows. Without
// write, it runs read-only and reports NeedsWrite instead of changing
// anything. Statements run one after another without a transaction, so a
// failing statement leaves the ones before it applied.
func execSQL(ctx context.Context, db *sql.DB, query string, write bool) (ExecResult, error) {
	res := ExecResult{QueryResult: QueryResult{Columns: []string{}, Rows: [][]any{}}}
	rows, err := db.QueryContext(ctx, query)
	if err != nil {
		if !write && strings.Contains(err.Error(), "readonly database") {
			res.NeedsWrite = true
			return res, nil
		}
		return res, err
	}
	cols, err := rows.Columns()
	if err != nil {
		rows.Close()
		return res, err
	}
	res.Columns = cols
	for rows.Next() {
		if len(res.Rows) == maxQueryRows {
			res.Truncated = true
			break
		}
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			rows.Close()
			return res, err
		}
		for i, v := range vals {
			vals[i] = cell(v)
		}
		res.Rows = append(res.Rows, vals)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return res, err
	}
	if write {
		db.QueryRowContext(ctx, `SELECT changes()`).Scan(&res.Changes)
	}
	return res, nil
}

// --- helper protocol ---

// HelperRequest is what `sideyard sqlite-query DB` reads from stdin.
type HelperRequest struct {
	// Op is schema, rows, mutate, alter or exec.
	Op       string       `json:"op"`
	SQL      string       `json:"sql,omitempty"`
	Write    bool         `json:"write,omitempty"`
	Rows     *RowsRequest `json:"rows,omitempty"`
	Mutation *Mutation    `json:"mutation,omitempty"`
	Alter    *Alter       `json:"alter,omitempty"`
}

type HelperResponse struct {
	Schema   []TableInfo     `json:"schema,omitempty"`
	Rows     *RowsResult     `json:"rows,omitempty"`
	Mutation *MutationResult `json:"mutation,omitempty"`
	Exec     *ExecResult     `json:"exec,omitempty"`
	Error    string          `json:"error,omitempty"`
}

// Query answers a HelperRequest; the platform calls it directly, or
// through the helper as the project's user.
func Query(ctx context.Context, path string, req HelperRequest) HelperResponse {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	write := req.Op == "mutate" || req.Op == "alter" || (req.Op == "exec" && req.Write)
	db, err := openFile(path, write)
	if err != nil {
		return HelperResponse{Error: err.Error()}
	}
	defer db.Close()
	var resp HelperResponse
	switch req.Op {
	case "schema":
		resp.Schema, err = schema(ctx, db)
	case "rows":
		if req.Rows == nil {
			return HelperResponse{Error: "missing rows request"}
		}
		var r RowsResult
		r, err = readRows(ctx, db, *req.Rows)
		resp.Rows = &r
	case "mutate":
		if req.Mutation == nil {
			return HelperResponse{Error: "missing mutation"}
		}
		var m MutationResult
		m, err = mutate(ctx, db, *req.Mutation)
		resp.Mutation = &m
	case "alter":
		if req.Alter == nil {
			return HelperResponse{Error: "missing change"}
		}
		var q string
		if q, err = AlterSQL(*req.Alter); err == nil {
			_, err = db.ExecContext(ctx, q)
		}
	case "exec":
		var e ExecResult
		e, err = execSQL(ctx, db, req.SQL, req.Write)
		resp.Exec = &e
	default:
		err = fmt.Errorf("unknown op %q", req.Op)
	}
	if err != nil {
		return HelperResponse{Error: err.Error()}
	}
	return resp
}

// RunHelper runs the commands Sideyard executes as a project's user, so
// SQLite's side files stay owned by the app:
//
//	sqlite-snapshot SRC DST   consistent copy of a live database
//	sqlite-query DB           HelperRequest on stdin, HelperResponse on stdout
//
// handled is false if args isn't one of them.
func RunHelper(args []string, stdin io.Reader, stdout io.Writer) (handled bool, err error) {
	if len(args) == 0 {
		return false, nil
	}
	switch args[0] {
	case "sqlite-snapshot":
		if len(args) != 3 {
			return true, errors.New("usage: sideyard sqlite-snapshot SRC DST")
		}
		return true, SnapshotFile(args[1], args[2])
	case "sqlite-query":
		if len(args) != 2 {
			return true, errors.New("usage: sideyard sqlite-query DB < request.json")
		}
		dec := json.NewDecoder(stdin)
		dec.UseNumber() // keep integers exact
		var req HelperRequest
		if err := dec.Decode(&req); err != nil {
			return true, err
		}
		return true, json.NewEncoder(stdout).Encode(Query(context.Background(), args[1], req))
	}
	return false, nil
}
