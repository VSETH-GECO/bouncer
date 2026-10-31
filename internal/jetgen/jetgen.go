// Package jetgen generates go-jet table and model code for the tables bouncer uses.
package jetgen

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/go-jet/jet/v2/generator/metadata"
	jetmysqlgen "github.com/go-jet/jet/v2/generator/mysql"
	"github.com/go-jet/jet/v2/generator/template"
	jetmysql "github.com/go-jet/jet/v2/mysql"
	"github.com/go-sql-driver/mysql"
)

// Tables is the allowlist of tables code is generated for, sorted. The database also holds FreeRADIUS and Kea tables
// bouncer never touches; skipping them keeps the generated tree small and avoids Kea tables the generator rejects.
var Tables = []string{
	"bouncer_jobs",
	"bouncer_log",
	"bouncer_switch_ip",
	"bouncer_switch_map",
	"bouncer_vlan",
	"bouncer_vlan_switch",
	"lease4",
	"lease6",
	"login_logs",
	"radacct",
	"radcheck",
	"radreply",
}

// Generate introspects the database behind dsn and writes code for Tables into destDir. Views are skipped.
func Generate(ctx context.Context, dsn, destDir string) error {
	cfg, err := mysql.ParseDSN(dsn)
	if err != nil {
		return fmt.Errorf("parsing dsn: %w", err)
	}
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		return fmt.Errorf("opening database: %w", err)
	}
	defer db.Close()
	if err := db.PingContext(ctx); err != nil {
		return fmt.Errorf("connecting to database: %w", err)
	}

	allowed := make(map[string]bool, len(Tables))
	for _, name := range Tables {
		allowed[name] = true
	}
	tmpl := template.Default(jetmysql.Dialect).UseSchema(func(schema metadata.Schema) template.Schema {
		return template.DefaultSchema(schema).
			UseModel(template.DefaultModel().
				UseTable(func(t metadata.Table) template.TableModel {
					m := template.DefaultTableModel(t)
					m.Skip = !allowed[t.Name]
					return m
				}).
				UseView(func(t metadata.Table) template.ViewModel {
					v := template.DefaultViewModel(t)
					v.Skip = true
					return v
				})).
			UseSQLBuilder(template.DefaultSQLBuilder().
				UseTable(func(t metadata.Table) template.TableSQLBuilder {
					b := template.DefaultTableSQLBuilder(t)
					b.Skip = !allowed[t.Name]
					return b
				}).
				UseView(func(t metadata.Table) template.ViewSQLBuilder {
					v := template.DefaultViewSQLBuilder(t)
					v.Skip = true
					return v
				}))
	})
	if err := jetmysqlgen.GenerateDB(db, cfg.DBName, destDir, tmpl); err != nil {
		return fmt.Errorf("generating code: %w", err)
	}
	return nil
}
