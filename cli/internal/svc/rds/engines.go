package rds

import (
	"fmt"
	"strings"
)

// Engine describes how to run, probe, back up and query one database engine.
type Engine struct {
	Name        string   `json:"name"`
	Label       string   `json:"label"`
	Kind        string   `json:"kind"` // relational | cache | document
	Versions    []string `json:"versions"`
	DefaultPort int      `json:"default_port"`
	DataDir     string   `json:"-"`
	image       func(version string) string
	env         func(user, pass, db string) map[string]string
	cmd         func(pass string) []string
	probe       func(user, pass, db string) []string
	dump        func(user, pass string) []string // writes a backup to stdout
	restore     func(user, pass string) []string // reads a backup from stdin
	query       func(user, pass, db, sql string) []string
	HasUsers    bool `json:"has_users"`
	HasDatabase bool `json:"has_database"`
	HasPassword bool `json:"has_password"`
}

func sh(script string) []string { return []string{"/bin/sh", "-c", script} }

// q single-quotes s for a POSIX shell.
func q(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

var engines = []Engine{
	{
		Name: "postgres", Label: "PostgreSQL", Kind: "relational", Versions: []string{"17", "16", "15", "14"}, DefaultPort: 5432,
		DataDir: "/var/lib/postgresql/data", HasUsers: true, HasDatabase: true, HasPassword: true,
		image: func(v string) string { return "postgres:" + v + "-alpine" },
		env: func(u, p, db string) map[string]string {
			return map[string]string{"POSTGRES_USER": u, "POSTGRES_PASSWORD": p, "POSTGRES_DB": db, "PGDATA": "/var/lib/postgresql/data/pgdata"}
		},
		probe: func(u, p, db string) []string { return []string{"pg_isready", "-h", "127.0.0.1", "-U", u, "-d", db} },
		dump: func(u, p string) []string {
			return sh("PGPASSWORD=" + q(p) + " pg_dumpall -h 127.0.0.1 -U " + q(u) + " --clean --if-exists")
		},
		restore: func(u, p string) []string {
			return sh("PGPASSWORD=" + q(p) + " psql -h 127.0.0.1 -U " + q(u) + " -d postgres -q -v ON_ERROR_STOP=0 >/dev/null")
		},
		query: func(u, p, db, sql string) []string {
			return sh("PGPASSWORD=" + q(p) + " psql -h 127.0.0.1 -U " + q(u) + " -d " + q(db) + " --csv -v ON_ERROR_STOP=1 -c " + q(sql))
		},
	},
	{
		Name: "mysql", Label: "MySQL", Kind: "relational", Versions: []string{"8.4", "8.0"}, DefaultPort: 3306,
		DataDir: "/var/lib/mysql", HasUsers: true, HasDatabase: true, HasPassword: true,
		image: func(v string) string { return "mysql:" + v },
		env: func(u, p, db string) map[string]string {
			return map[string]string{"MYSQL_ROOT_PASSWORD": p, "MYSQL_USER": u, "MYSQL_PASSWORD": p, "MYSQL_DATABASE": db}
		},
		probe: func(u, p, db string) []string {
			return sh("MYSQL_PWD=" + q(p) + " mysqladmin ping -h127.0.0.1 -uroot --silent")
		},
		dump: func(u, p string) []string {
			return sh("MYSQL_PWD=" + q(p) + " mysqldump -h127.0.0.1 -uroot --all-databases --single-transaction --routines --events")
		},
		restore: func(u, p string) []string { return sh("MYSQL_PWD=" + q(p) + " mysql -h127.0.0.1 -uroot") },
		query: func(u, p, db, sql string) []string {
			return sh("MYSQL_PWD=" + q(p) + " mysql -h127.0.0.1 -uroot -D " + q(db) + " --batch -e " + q(sql))
		},
	},
	{
		Name: "mariadb", Label: "MariaDB", Kind: "relational", Versions: []string{"11.4", "10.11"}, DefaultPort: 3306,
		DataDir: "/var/lib/mysql", HasUsers: true, HasDatabase: true, HasPassword: true,
		image: func(v string) string { return "mariadb:" + v },
		env: func(u, p, db string) map[string]string {
			return map[string]string{"MARIADB_ROOT_PASSWORD": p, "MARIADB_USER": u, "MARIADB_PASSWORD": p, "MARIADB_DATABASE": db}
		},
		probe: func(u, p, db string) []string {
			return sh("MYSQL_PWD=" + q(p) + " mariadb-admin ping -h127.0.0.1 -uroot --silent")
		},
		dump: func(u, p string) []string {
			return sh("MYSQL_PWD=" + q(p) + " mariadb-dump -h127.0.0.1 -uroot --all-databases --single-transaction --routines --events")
		},
		restore: func(u, p string) []string { return sh("MYSQL_PWD=" + q(p) + " mariadb -h127.0.0.1 -uroot") },
		query: func(u, p, db, sql string) []string {
			return sh("MYSQL_PWD=" + q(p) + " mariadb -h127.0.0.1 -uroot -D " + q(db) + " --batch -e " + q(sql))
		},
	},
	{
		Name: "redis", Label: "Redis", Kind: "cache", Versions: []string{"7.4", "7.2", "7.1", "7.0", "6.2"}, DefaultPort: 6379, DataDir: "/data", HasPassword: true,
		image: func(v string) string { return "redis:" + v + "-alpine" },
		cmd:   func(p string) []string { return keyValueCmd("redis-server", p) },
		probe: func(u, p, db string) []string { return sh("redis-cli" + authFlags(p) + " ping | grep -q PONG") },
		dump: func(u, p string) []string {
			return sh("redis-cli" + authFlags(p) + " --rdb /tmp/hc-snapshot.rdb >/dev/null 2>&1 && cat /tmp/hc-snapshot.rdb && rm -f /tmp/hc-snapshot.rdb")
		},
		query: func(u, p, db, cmd string) []string {
			return append(append([]string{"redis-cli"}, authArgs(p)...), splitCommand(cmd)...)
		},
	},
	{
		Name: "valkey", Label: "Valkey", Kind: "cache", Versions: []string{"8", "7.2"}, DefaultPort: 6379, DataDir: "/data", HasPassword: true,
		image: func(v string) string { return "valkey/valkey:" + v + "-alpine" },
		cmd:   func(p string) []string { return keyValueCmd("valkey-server", p) },
		probe: func(u, p, db string) []string { return sh("valkey-cli" + authFlags(p) + " ping | grep -q PONG") },
		dump: func(u, p string) []string {
			return sh("valkey-cli" + authFlags(p) + " --rdb /tmp/hc-snapshot.rdb >/dev/null 2>&1 && cat /tmp/hc-snapshot.rdb && rm -f /tmp/hc-snapshot.rdb")
		},
		query: func(u, p, db, cmd string) []string {
			return append(append([]string{"valkey-cli"}, authArgs(p)...), splitCommand(cmd)...)
		},
	},
	{
		Name: "memcached", Label: "Memcached", Kind: "cache", Versions: []string{"1.6"}, DefaultPort: 11211,
		image: func(v string) string { return "memcached:" + v + "-alpine" },
		probe: func(u, p, db string) []string { return []string{"/bin/true"} },
	},
	{
		Name: "mongodb", Label: "MongoDB (DocumentDB compatible)", Kind: "document", Versions: []string{"8.0", "7.0"}, DefaultPort: 27017,
		DataDir: "/data/db", HasUsers: true, HasDatabase: true, HasPassword: true,
		image: func(v string) string { return "mongo:" + v },
		env: func(u, p, db string) map[string]string {
			return map[string]string{"MONGO_INITDB_ROOT_USERNAME": u, "MONGO_INITDB_ROOT_PASSWORD": p, "MONGO_INITDB_DATABASE": db}
		},
		probe: func(u, p, db string) []string {
			return sh("mongosh --quiet --host 127.0.0.1 -u " + q(u) + " -p " + q(p) + " --authenticationDatabase admin --eval 'db.runCommand({ping:1}).ok' | grep -q 1")
		},
		dump: func(u, p string) []string {
			return sh("mongodump --quiet --host 127.0.0.1 -u " + q(u) + " -p " + q(p) + " --authenticationDatabase admin --archive --gzip")
		},
		restore: func(u, p string) []string {
			return sh("mongorestore --quiet --host 127.0.0.1 -u " + q(u) + " -p " + q(p) + " --authenticationDatabase admin --archive --gzip --drop")
		},
		query: func(u, p, db, js string) []string {
			return sh("mongosh --quiet --host 127.0.0.1 -u " + q(u) + " -p " + q(p) + " --authenticationDatabase admin " + q(db) + " --eval " + q(js))
		},
	},
}

// A Redis-family server runs with a password (the native API always sets one)
// or without (an ElastiCache cluster created without an AuthToken).

func keyValueCmd(server, pass string) []string {
	if pass == "" {
		return []string{server, "--appendonly", "yes"}
	}
	return []string{server, "--requirepass", pass, "--appendonly", "yes"}
}

// authArgs are the redis-cli authentication arguments for a password ("" for none).
func authArgs(pass string) []string {
	if pass == "" {
		return nil
	}
	return []string{"--no-auth-warning", "-a", pass}
}

// authFlags is authArgs quoted for a shell command line.
func authFlags(pass string) string {
	if pass == "" {
		return ""
	}
	return " --no-auth-warning -a " + q(pass)
}

// splitCommand splits a Redis-style command line into arguments, honouring
// single and double quotes, so it can be executed without a shell.
func splitCommand(s string) []string {
	var args []string
	var cur strings.Builder
	var quote rune
	inArg := false
	for _, r := range s {
		switch {
		case quote != 0 && r == quote:
			quote = 0
		case quote != 0:
			cur.WriteRune(r)
		case r == '"' || r == '\'':
			quote, inArg = r, true
		case r == ' ' || r == '\t' || r == '\n':
			if inArg {
				args = append(args, cur.String())
				cur.Reset()
				inArg = false
			}
		default:
			cur.WriteRune(r)
			inArg = true
		}
	}
	if inArg {
		args = append(args, cur.String())
	}
	return args
}

func findEngine(name string) (Engine, bool) {
	for _, e := range engines {
		if e.Name == name {
			return e, true
		}
	}
	return Engine{}, false
}

type InstanceClass struct {
	Name     string  `json:"name"`
	VCPUs    float64 `json:"vcpus"`
	MemoryMB int64   `json:"memory_mb"`
	Kind     string  `json:"kind"` // db | cache
}

var classes = []InstanceClass{
	{"db.t3.micro", 1, 1024, "db"},
	{"db.t3.small", 1, 2048, "db"},
	{"db.t3.medium", 2, 4096, "db"},
	{"db.t3.large", 2, 8192, "db"},
	{"db.m5.large", 2, 8192, "db"},
	{"db.m5.xlarge", 4, 16384, "db"},
	{"db.r5.large", 2, 16384, "db"},
	{"cache.t2.micro", 1, 512, "cache"},
	{"cache.t2.small", 1, 1536, "cache"},
	{"cache.t2.medium", 2, 3072, "cache"},
	{"cache.t3.micro", 1, 512, "cache"},
	{"cache.t3.small", 1, 1536, "cache"},
	{"cache.t3.medium", 2, 3072, "cache"},
	{"cache.t4g.micro", 1, 512, "cache"},
	{"cache.t4g.small", 1, 1536, "cache"},
	{"cache.t4g.medium", 2, 3072, "cache"},
	{"cache.m5.large", 2, 6144, "cache"},
	{"cache.m5.xlarge", 4, 12288, "cache"},
	{"cache.m6g.large", 2, 6144, "cache"},
	{"cache.r5.large", 2, 13312, "cache"},
	{"cache.r6g.large", 2, 13312, "cache"},
}

func findClass(name string) (InstanceClass, bool) {
	for _, c := range classes {
		if c.Name == name {
			return c, true
		}
	}
	return InstanceClass{}, false
}

func (e Engine) defaultClass() string {
	if e.Kind == "cache" {
		return "cache.t3.micro"
	}
	return "db.t3.micro"
}

func (e Engine) validVersion(v string) error {
	for _, x := range e.Versions {
		if x == v || strings.HasPrefix(v, x+".") {
			return nil
		}
	}
	return fmt.Errorf("engine %s supports versions %s", e.Name, strings.Join(e.Versions, ", "))
}
