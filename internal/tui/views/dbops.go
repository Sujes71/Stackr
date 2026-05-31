package views

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/stackr/stackr/internal/config"
	"github.com/stackr/stackr/internal/docker"
	"github.com/stackr/stackr/internal/tui/styles"
)

type dbOpsAction int

const (
	dbActionExportSchema dbOpsAction = iota
	dbActionExportData
	dbActionImportSchema
	dbActionImportData
	dbActionSyncFromEnv
)

type dbOpItem struct {
	label  string
	desc   string
	action dbOpsAction
	dbType string // "" for generic actions (reset)
}

type DBOpsModel struct {
	cfg         *config.Config
	profile     *config.Profile
	envName     string
	serviceName string // selected service; "" means show all
	cursor      int
	running     bool
	lastResult  string
	log         []string
}

func NewDBOps(cfg *config.Config, p *config.Profile, envName, serviceName string) *DBOpsModel {
	return &DBOpsModel{cfg: cfg, profile: p, envName: envName, serviceName: serviceName}
}

type dbOpsDoneMsg struct {
	result string
	err    error
}

type dbOpsLogMsg string

func (m *DBOpsModel) Init() tea.Cmd { return nil }

func (m *DBOpsModel) actions() []dbOpItem {
	env := m.findEnv(m.envName)
	if env == nil {
		return nil
	}

	// Determine the DB type for the selected service
	filterType := ""
	if m.serviceName != "" {
		for _, svc := range env.Services {
			if svc.Name == m.serviceName {
				filterType = svc.DBType()
				break
			}
		}
	}

	var items []dbOpItem

	// DB-specific actions — only for the selected service's DB type
	if filterType != "" {
		m.appendDBActions(&items, filterType, env)
	}

	return items
}

func (m *DBOpsModel) appendDBActions(items *[]dbOpItem, dt string, env *config.Environment) {
	switch dt {
	case "postgres":
		*items = append(*items,
			dbOpItem{label: "Export schema (" + dt + ")", desc: "pg_dump -s", action: dbActionExportSchema, dbType: dt},
			dbOpItem{label: "Export data (" + dt + ")", desc: "pg_dump -a", action: dbActionExportData, dbType: dt},
			dbOpItem{label: "Import schema (" + dt + ")", desc: "psql ← schema.sql", action: dbActionImportSchema, dbType: dt},
			dbOpItem{label: "Import data (" + dt + ")", desc: "psql ← data.sql", action: dbActionImportData, dbType: dt},
		)
		if m.findEnv("dev") != nil && env.Name != "dev" {
			*items = append(*items, dbOpItem{
				label: "Sync from dev (" + dt + ")", desc: "export dev → import here",
				action: dbActionSyncFromEnv, dbType: dt,
			})
		}

	case "mongo":
		*items = append(*items,
			dbOpItem{label: "Export data (" + dt + ")", desc: "mongodump → dump/", action: dbActionExportData, dbType: dt},
			dbOpItem{label: "Import data (" + dt + ")", desc: "mongorestore ← dump/", action: dbActionImportData, dbType: dt},
		)

	case "mysql":
		*items = append(*items,
			dbOpItem{label: "Export data (" + dt + ")", desc: "mysqldump → data.sql", action: dbActionExportData, dbType: dt},
			dbOpItem{label: "Import data (" + dt + ")", desc: "mysql ← data.sql", action: dbActionImportData, dbType: dt},
		)

	case "mssql":
		*items = append(*items,
			dbOpItem{label: "Export data (" + dt + ")", desc: "sqlpackage export → .bacpac", action: dbActionExportData, dbType: dt},
			dbOpItem{label: "Import data (" + dt + ")", desc: "sqlpackage import ← .bacpac", action: dbActionImportData, dbType: dt},
		)
	}
}

var dbOpsUp = key.NewBinding(key.WithKeys("up", "k"))
var dbOpsDown = key.NewBinding(key.WithKeys("down", "j"))
var dbOpsRun = key.NewBinding(key.WithKeys("enter"))
var dbOpsBack = key.NewBinding(key.WithKeys("esc"))

func (m *DBOpsModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	acts := m.actions()
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch {
		case key.Matches(msg, dbOpsBack):
			return m, func() tea.Msg {
				return NavigateTo{Screen: NavProfileView, ProfileName: m.profile.Name}
			}
		case key.Matches(msg, dbOpsUp) && m.cursor > 0:
			m.cursor--
		case key.Matches(msg, dbOpsDown) && m.cursor < len(acts)-1:
			m.cursor++
		case key.Matches(msg, dbOpsRun) && !m.running && len(acts) > 0:
			return m, m.runAction(acts[m.cursor])
		}
	case dbOpsDoneMsg:
		m.running = false
		if msg.err != nil {
			m.lastResult = styles.StyleError.Render("✗ " + msg.err.Error())
		} else {
			m.lastResult = styles.StyleSuccess.Render("✓ " + msg.result)
		}
		return m, ClearActionAfter(3 * time.Second)

	case ClearActionMsg:
		m.lastResult = ""

	case dbOpsLogMsg:
		m.log = append(m.log, string(msg))
		if len(m.log) > 12 {
			m.log = m.log[len(m.log)-12:]
		}
	}
	return m, nil
}

func (m *DBOpsModel) runAction(item dbOpItem) tea.Cmd {
	m.running = true
	m.lastResult = ""
	m.log = nil
	env := m.findEnv(m.envName)

	return func() tea.Msg {
		dumpDir := filepath.Join(config.ProfileDir(m.profile.Name), m.envName, "dumps")

		switch item.action {
		case dbActionExportSchema:
			return m.runPostgresAction(env, item.dbType, func(pg *config.PostgresOps) tea.Msg {
				path := filepath.Join(dumpDir, "schema-"+item.dbType+"-"+ts()+".sql")
				if err := docker.PgDump(pg, path, "-s"); err != nil {
					return dbOpsDoneMsg{err: err}
				}
				return dbOpsDoneMsg{result: "Schema exported to " + path}
			})

		case dbActionExportData:
			switch item.dbType {
			case "postgres":
				return m.runPostgresAction(env, item.dbType, func(pg *config.PostgresOps) tea.Msg {
					path := filepath.Join(dumpDir, "data-"+item.dbType+"-"+ts()+".sql")
					if err := docker.PgDump(pg, path, "-a"); err != nil {
						return dbOpsDoneMsg{err: err}
					}
					return dbOpsDoneMsg{result: "Data exported to " + path}
				})
			case "mongo":
				return m.runMongoAction(env, item.dbType, func(mg *config.MongoOps) tea.Msg {
					path := filepath.Join(dumpDir, ts())
					if err := docker.MongoDump(mg, path); err != nil {
						return dbOpsDoneMsg{err: err}
					}
					return dbOpsDoneMsg{result: "Data exported to " + path}
				})
			case "mysql":
				return m.runMySqlAction(env, item.dbType, func(my *config.MySqlOps) tea.Msg {
					path := filepath.Join(dumpDir, "data-"+item.dbType+"-"+ts()+".sql")
					if err := docker.MySqlDump(my, path); err != nil {
						return dbOpsDoneMsg{err: err}
					}
					return dbOpsDoneMsg{result: "Data exported to " + path}
				})
			case "mssql":
				return dbOpsDoneMsg{result: "MS SQL export requires sqlpackage CLI. Run manually:\n  sqlpackage /Action:Export /SourceServerName:<host> /SourceDatabaseName:<db> /TargetFile:<file>.bacpac"}
			}

		case dbActionImportSchema:
			return m.runPostgresAction(env, item.dbType, func(pg *config.PostgresOps) tea.Msg {
				// Use latest schema dump in the dump dir
				return dbOpsDoneMsg{err: fmt.Errorf("import schema: pick a dump file and run: psql -h %s -U %s -d %s -f <file>", pg.Host, pg.User, pg.Database)}
			})

		case dbActionImportData:
			switch item.dbType {
			case "postgres":
				return m.runPostgresAction(env, item.dbType, func(pg *config.PostgresOps) tea.Msg {
					return dbOpsDoneMsg{err: fmt.Errorf("import data: psql -h %s -U %s -d %s -f <dump.sql>", pg.Host, pg.User, pg.Database)}
				})
			case "mongo":
				return m.runMongoAction(env, item.dbType, func(mg *config.MongoOps) tea.Msg {
					return dbOpsDoneMsg{err: fmt.Errorf("import data: mongorestore --drop --dir <dump_dir>")}
				})
			case "mysql":
				return m.runMySqlAction(env, item.dbType, func(my *config.MySqlOps) tea.Msg {
					return dbOpsDoneMsg{err: fmt.Errorf("import data: mysql -h %s -P %s -u %s -p %s < dump.sql", my.Host, my.Port, my.User, my.Database)}
				})
			}

		case dbActionSyncFromEnv:
			devEnv := m.findEnv("dev")
			localEnv := m.findEnv("local")
			if devEnv == nil || devEnv.DBOps == nil || devEnv.DBOps.Postgres == nil {
				return dbOpsDoneMsg{err: fmt.Errorf("no postgres db_ops config in dev env")}
			}
			if localEnv == nil || localEnv.DBOps == nil || localEnv.DBOps.Postgres == nil {
				return dbOpsDoneMsg{err: fmt.Errorf("no postgres db_ops config in local env")}
			}
			ts := time.Now().Format("20060102-150405")
			tmpSchema := filepath.Join(dumpDir, "sync-schema-"+ts+".sql")
			tmpData := filepath.Join(dumpDir, "sync-data-"+ts+".sql")
			if err := docker.PgDump(devEnv.DBOps.Postgres, tmpSchema, "-s"); err != nil {
				return dbOpsDoneMsg{err: fmt.Errorf("export schema from dev: %w", err)}
			}
			if err := docker.PgDump(devEnv.DBOps.Postgres, tmpData, "-a"); err != nil {
				return dbOpsDoneMsg{err: fmt.Errorf("export data from dev: %w", err)}
			}
			if err := docker.PgRestore(localEnv.DBOps.Postgres, tmpSchema); err != nil {
				return dbOpsDoneMsg{err: fmt.Errorf("import schema to local: %w", err)}
			}
			if err := docker.PgRestore(localEnv.DBOps.Postgres, tmpData); err != nil {
				return dbOpsDoneMsg{err: fmt.Errorf("import data to local: %w", err)}
			}
			return dbOpsDoneMsg{result: "Synced dev → local successfully"}
		}
		return dbOpsDoneMsg{err: fmt.Errorf("unknown action")}
	}
}

func (m *DBOpsModel) runPostgresAction(env *config.Environment, dbType string, fn func(*config.PostgresOps) tea.Msg) tea.Msg {
	if env == nil || env.DBOps == nil || env.DBOps.Postgres == nil {
		return dbOpsDoneMsg{err: fmt.Errorf("no postgres db_ops config in %s/%s", m.profile.Name, m.envName)}
	}
	return fn(env.DBOps.Postgres)
}

func (m *DBOpsModel) runMongoAction(env *config.Environment, dbType string, fn func(*config.MongoOps) tea.Msg) tea.Msg {
	if env == nil || env.DBOps == nil || env.DBOps.Mongo == nil {
		return dbOpsDoneMsg{err: fmt.Errorf("no mongo db_ops config in %s/%s", m.profile.Name, m.envName)}
	}
	return fn(env.DBOps.Mongo)
}

func (m *DBOpsModel) runMySqlAction(env *config.Environment, dbType string, fn func(*config.MySqlOps) tea.Msg) tea.Msg {
	if env == nil || env.DBOps == nil || env.DBOps.MySQL == nil {
		return dbOpsDoneMsg{err: fmt.Errorf("no mysql db_ops config in %s/%s", m.profile.Name, m.envName)}
	}
	return fn(env.DBOps.MySQL)
}

func (m *DBOpsModel) findEnv(name string) *config.Environment {
	for i := range m.profile.Environments {
		if m.profile.Environments[i].Name == name {
			return &m.profile.Environments[i]
		}
	}
	return nil
}

func ts() string {
	return time.Now().Format("20060102-150405")
}

func (m *DBOpsModel) View() string {
	acts := m.actions()

	var b strings.Builder
	b.WriteString("\n")
	if m.serviceName != "" {
		b.WriteString(styles.StyleTitle.Render(
			fmt.Sprintf(" DB Ops — %s / %s / %s ", m.profile.Name, m.envName, m.serviceName)))
	} else {
		b.WriteString(styles.StyleTitle.Render(
			fmt.Sprintf(" DB Ops — %s / %s ", m.profile.Name, m.envName)))
	}
	b.WriteString("\n\n")

	if len(acts) == 0 {
		b.WriteString(styles.StyleSubtitle.Render("  No database operations available for this service.\n"))
	} else {
		for i, item := range acts {
			label := fmt.Sprintf("%-24s  %s", item.label, styles.StyleSubtitle.Render(item.desc))
			if i == m.cursor {
				b.WriteString(styles.StyleListItemSelected.Render(label))
			} else {
				b.WriteString(styles.StyleListItem.Render(label))
			}
			b.WriteString("\n")
		}
	}

	if m.running {
		b.WriteString("\n" + styles.StyleSubtitle.Render("  Running…"))
	}

	if m.lastResult != "" {
		b.WriteString("\n  " + m.lastResult)
	}

	if len(m.log) > 0 {
		b.WriteString("\n\n")
		b.WriteString(styles.StyleSubtitle.Render("  Output:\n"))
		for _, line := range m.log {
			b.WriteString(styles.StyleCode.Render("  "+line) + "\n")
		}
	}

	b.WriteString("\n\n")
	b.WriteString(styles.StyleHelp.Render(
		styles.HelpItem("↑↓", "select") + "   " +
			styles.HelpItem("↵", "run") + "   " +
			styles.HelpItem("esc", "back"),
	))
	return b.String()
}
