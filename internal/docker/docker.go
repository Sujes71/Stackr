package docker

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/stackr/stackr/internal/config"
)

func composePath(profileName, envName string) string {
	return filepath.Join(config.ProfileDir(profileName), envName, "docker-compose.yml")
}

func composeArgs(profileName, envName string, args ...string) []string {
	base := []string{"compose", "-f", composePath(profileName, envName)}
	return append(base, args...)
}

// runDocker executes a docker command and returns a friendly error on failure.
func runDocker(args ...string) error {
	cmd := exec.Command("docker", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return friendlyError(err, string(out))
	}
	return nil
}

// friendlyError wraps an exec error with the command's stderr output.
// When the docker daemon is not running it shows a clear message instead of "exit status 1".
func friendlyError(err error, stderr string) error {
	stderr = strings.TrimSpace(stderr)

	// Also check the error string itself for common patterns
	errMsg := err.Error()

	switch {
	case strings.Contains(stderr, "Cannot connect to the Docker daemon"),
		strings.Contains(stderr, "failed to connect to the docker API"),
		strings.Contains(stderr, "dial unix"),
		strings.Contains(stderr, "Is the docker daemon running"),
		strings.Contains(errMsg, "dial unix"),
		strings.Contains(errMsg, "connection refused"):
		return fmt.Errorf("Docker daemon is not running. Start Docker Desktop or the daemon and try again.")

	case strings.Contains(stderr, "no such file or directory") && strings.Contains(stderr, "docker-compose.yml"):
		return fmt.Errorf("Compose file not found for this environment. Add services first.")

	default:
		if stderr != "" {
			return fmt.Errorf("%s", stderr)
		}
		return err
	}
}

// ServiceStatus represents the running state of a single container.
type ServiceStatus struct {
	Name   string
	State  string
	Ports  string
	Health string
}

func Status(profileName, envName string) ([]ServiceStatus, error) {
	args := composeArgs(profileName, envName, "ps", "--format", "json")
	out, err := exec.Command("docker", args...).Output()
	if err != nil {
		return []ServiceStatus{}, nil
	}
	var statuses []ServiceStatus
	scanner := bufio.NewScanner(strings.NewReader(string(out)))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		statuses = append(statuses, ServiceStatus{
			Name:   extractJSON(line, "Name"),
			State:  extractJSON(line, "State"),
			Ports:  extractJSON(line, "Publishers"),
			Health: extractJSON(line, "Health"),
		})
	}
	return statuses, nil
}

func extractJSON(s, key string) string {
	search := fmt.Sprintf(`"%s":"`, key)
	idx := strings.Index(s, search)
	if idx < 0 {
		return ""
	}
	start := idx + len(search)
	end := strings.Index(s[start:], `"`)
	if end < 0 {
		return ""
	}
	return s[start : start+end]
}

func Up(profileName, envName string) error {
	return runDocker(composeArgs(profileName, envName, "up", "-d")...)
}

func UpService(profileName, envName, serviceName string) error {
	return runDocker(composeArgs(profileName, envName, "up", "-d", serviceName)...)
}

func Down(profileName, envName string) error {
	return runDocker(composeArgs(profileName, envName, "down")...)
}

func DownService(profileName, envName, serviceName string) error {
	return runDocker(composeArgs(profileName, envName, "stop", serviceName)...)
}

func DownVolumes(profileName, envName string) error {
	return runDocker(composeArgs(profileName, envName, "down", "-v")...)
}

func Restart(profileName, envName, service string) error {
	return runDocker(composeArgs(profileName, envName, "restart", service)...)
}

func UpAndStream(profileName, envName string, ch chan<- string) error {
	args := composeArgs(profileName, envName, "up", "-d", "--remove-orphans")
	cmd := exec.Command("docker", args...)
	stderr, _ := cmd.StderrPipe()
	if err := cmd.Start(); err != nil {
		close(ch)
		return err
	}
	scanner := bufio.NewScanner(stderr)
	for scanner.Scan() {
		ch <- scanner.Text()
	}
	err := cmd.Wait()
	close(ch)
	if err != nil {
		return friendlyError(err, "")
	}
	return nil
}

func Logs(profileName, envName string, follow bool, ch chan<- string, done <-chan struct{}, svcName ...string) {
	args := composeArgs(profileName, envName, "logs", "--tail=200")
	if follow {
		args = append(args, "-f")
	}
	args = append(args, svcName...)
	cmd := exec.Command("docker", args...)
	stdout, _ := cmd.StdoutPipe()
	if err := cmd.Start(); err != nil {
		ch <- "error: " + err.Error()
		close(ch)
		return
	}
	go func() {
		<-done
		cmd.Process.Kill()
	}()
	scanner := bufio.NewScanner(stdout)
	for scanner.Scan() {
		ch <- scanner.Text()
	}
	cmd.Wait()
	close(ch)
}

// LocalImage represents a Docker image present on the local daemon.
type LocalImage struct {
	Repository string
	Tag        string
	ID         string
	Size       string
}

func LocalImages() ([]LocalImage, error) {
	out, err := exec.Command("docker", "images", "--format",
		"{{.Repository}}\t{{.Tag}}\t{{.ID}}\t{{.Size}}").Output()
	if err != nil {
		return nil, err
	}
	var images []LocalImage
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		parts := strings.Split(line, "\t")
		if len(parts) < 4 {
			continue
		}
		images = append(images, LocalImage{parts[0], parts[1], parts[2], parts[3]})
	}
	return images, nil
}

// PgDump runs pg_dump. flag: "-s" schema, "-a" data, "" full.
func PgDump(syncEnv *config.SyncEnv, outPath, flag string) error {
	args := []string{"-h", syncEnv.Host, "-p", syncEnv.Port, "-U", syncEnv.User}
	if flag != "" {
		args = append(args, flag)
	}
	args = append(args, syncEnv.Database)
	cmd := exec.Command("pg_dump", args...)
	cmd.Env = append(os.Environ(), "PGPASSWORD="+syncEnv.Password)
	out, err := cmd.Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return fmt.Errorf("pg_dump: %s: %w", strings.TrimSpace(string(exitErr.Stderr)), err)
		}
		return fmt.Errorf("pg_dump: %w", err)
	}
	return os.WriteFile(outPath, out, 0644)
}

// MongoDump runs mongodump.
func MongoDump(syncEnv *config.SyncEnv, outDir string) error {
	cmd := exec.Command("mongodump",
		"--host", syncEnv.Host+":"+syncEnv.Port,
		"--username", syncEnv.User,
		"--password", syncEnv.Password,
		"--authenticationDatabase", syncEnv.AuthDB,
		"--db", syncEnv.Database,
		"--out", outDir,
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("mongodump: %s: %w", strings.TrimSpace(string(out)), err)
	}
	return nil
}

// MySqlDump runs mysqldump.
func MySqlDump(syncEnv *config.SyncEnv, outPath string) error {
	args := []string{"-h", syncEnv.Host, "-P", syncEnv.Port, "-u", syncEnv.User, syncEnv.Database}
	cmd := exec.Command("mysqldump", args...)
	cmd.Env = append(os.Environ(), "MYSQL_PWD="+syncEnv.Password)
	out, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("mysqldump: %w", err)
	}
	return os.WriteFile(outPath, out, 0644)
}

// ================================================================
// Container helpers (for import via docker exec)
// ================================================================

// ContainerName returns the predictable container name for a service.
func ContainerName(profileName, envName, serviceName string) string {
	return fmt.Sprintf("stackr-%s-%s-%s", profileName, envName, config.SanitizeComposeName(serviceName))
}

// ContainerEnv reads a single environment variable from a running container.
func ContainerEnv(container, envVar string) (string, error) {
	out, err := exec.Command("docker", "exec", container, "printenv", envVar).Output()
	if err != nil {
		return "", fmt.Errorf("docker exec printenv %s: %w", envVar, err)
	}
	return strings.TrimSpace(string(out)), nil
}

// ContainerCopy copies a local file into the container.
func ContainerCopy(container, srcPath, destPath string) error {
	return exec.Command("docker", "cp", srcPath, container+":"+destPath).Run()
}

// ContainerExec runs an arbitrary command inside the container.
func ContainerExec(container string, args ...string) (string, error) {
	cmd := exec.Command("docker", append([]string{"exec", container}, args...)...)
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("docker exec: %w", err)
	}
	return string(out), nil
}

// ContainerExecWithEnv runs a command inside the container with env vars set via -e flags.
func ContainerExecWithEnv(container string, env []string, args ...string) (string, error) {
	dockerArgs := []string{"exec"}
	for _, e := range env {
		dockerArgs = append(dockerArgs, "-e", e)
	}
	dockerArgs = append(dockerArgs, container)
	dockerArgs = append(dockerArgs, args...)
	cmd := exec.Command("docker", dockerArgs...)
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("docker exec: %w", err)
	}
	return string(out), nil
}

// ToolAvailable checks if a CLI tool is in PATH.
func ToolAvailable(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}

// HasDumpFile returns true if any file matching the given glob pattern exists in the directory.
func HasDumpFile(dumpDir, pattern string) bool {
	matches, err := filepath.Glob(filepath.Join(dumpDir, pattern))
	if err != nil {
		return false
	}
	return len(matches) > 0
}

// HasAnyDump returns true if any file matching any of the given patterns exists in the directory.
func HasAnyDump(dumpDir string, patterns []string) bool {
	for _, p := range patterns {
		if HasDumpFile(dumpDir, p) {
			return true
		}
	}
	return false
}

// FindLatestDump returns the most recent dump file matching the given pattern
// in the dump directory. Pattern "" means check for subdirectories (mongo).
// Returns the full path to the most recent dump.
func FindLatestDump(dumpDir, pattern string) (string, error) {
	if pattern == "" {
		entries, err := os.ReadDir(dumpDir)
		if err != nil {
			return "", err
		}
		for _, e := range entries {
			if e.IsDir() {
				return filepath.Join(dumpDir, e.Name()), nil
			}
		}
		return "", fmt.Errorf("no dump directories found in %s", dumpDir)
	}
	matches, err := filepath.Glob(filepath.Join(dumpDir, pattern))
	if err != nil {
		return "", err
	}
	if len(matches) == 0 {
		return "", fmt.Errorf("no files matching %q in %s", pattern, dumpDir)
	}
	sort.Slice(matches, func(i, j int) bool {
		fi, _ := os.Stat(matches[i])
		fj, _ := os.Stat(matches[j])
		if fi == nil || fj == nil {
			return false
		}
		return fi.ModTime().After(fj.ModTime())
	})
	return matches[0], nil
}

// ================================================================
// Import functions (docker exec based)
// ================================================================

// ExecPgRestore restores a Postgres dump file into the container via docker exec.
// Steps:
//  1. Read POSTGRES_USER, POSTGRES_PASSWORD, POSTGRES_DB from container env
//  2. docker cp the dump file into /tmp/
//  3. docker exec psql -U <user> -d <db> -f /tmp/<file>
func ExecPgRestore(profileName, envName, serviceName, dumpPath string) error {
	container := ContainerName(profileName, envName, serviceName)

	user, err := ContainerEnv(container, "POSTGRES_USER")
	if err != nil {
		return fmt.Errorf("read POSTGRES_USER: %w", err)
	}
	pass, _ := ContainerEnv(container, "POSTGRES_PASSWORD")
	db, err := ContainerEnv(container, "POSTGRES_DB")
	if err != nil {
		return fmt.Errorf("read POSTGRES_DB: %w", err)
	}

	filename := filepath.Base(dumpPath)
	tmpPath := "/tmp/" + filename
	if err := ContainerCopy(container, dumpPath, tmpPath); err != nil {
		return fmt.Errorf("docker cp: %w", err)
	}

	env := []string{}
	if pass != "" {
		env = append(env, "PGPASSWORD="+pass)
	}
	_, err = ContainerExecWithEnv(container, env, "psql", "-U", user, "-d", db, "-f", tmpPath)
	return err
}

// ExecMongoRestore restores a MongoDB dump directory into the container via docker exec.
func ExecMongoRestore(profileName, envName, serviceName, dumpDir string) error {
	container := ContainerName(profileName, envName, serviceName)

	tmpDir := "/tmp/" + filepath.Base(dumpDir)
	if err := ContainerCopy(container, dumpDir, tmpDir); err != nil {
		return fmt.Errorf("docker cp: %w", err)
	}

	_, err := ContainerExec(container, "mongorestore", "--drop", "--dir", tmpDir)
	return err
}

// ExecMySqlRestore restores a MySQL dump file into the container via docker exec.
func ExecMySqlRestore(profileName, envName, serviceName, dumpPath string) error {
	container := ContainerName(profileName, envName, serviceName)

	user, err := ContainerEnv(container, "MYSQL_ROOT_USER")
	if err != nil {
		user, err = ContainerEnv(container, "MYSQL_USER")
		if err != nil {
			return fmt.Errorf("read MYSQL_USER: %w", err)
		}
	}
	pass, _ := ContainerEnv(container, "MYSQL_ROOT_PASSWORD")
	if pass == "" {
		pass, _ = ContainerEnv(container, "MYSQL_PASSWORD")
	}
	db, err := ContainerEnv(container, "MYSQL_DATABASE")
	if err != nil {
		return fmt.Errorf("read MYSQL_DATABASE: %w", err)
	}

	filename := filepath.Base(dumpPath)
	tmpPath := "/tmp/" + filename
	if err := ContainerCopy(container, dumpPath, tmpPath); err != nil {
		return fmt.Errorf("docker cp: %w", err)
	}

	_, err = ContainerExec(container, "sh", "-c",
		fmt.Sprintf("mysql -u %s -p'%s' %s < %s", user, pass, db, tmpPath))
	return err
}

// ================================================================
// SqlPackage helpers (MSSQL — runs on host, targets container port)
// ================================================================

// ================================================================
// Truncate functions (docker exec based)
// ================================================================

// PgTruncate truncates all tables in the public schema of a Postgres container.
func PgTruncate(profileName, envName, serviceName string) error {
	container := ContainerName(profileName, envName, serviceName)

	user, err := ContainerEnv(container, "POSTGRES_USER")
	if err != nil {
		return fmt.Errorf("read POSTGRES_USER: %w", err)
	}
	pass, _ := ContainerEnv(container, "POSTGRES_PASSWORD")
	db, err := ContainerEnv(container, "POSTGRES_DB")
	if err != nil {
		return fmt.Errorf("read POSTGRES_DB: %w", err)
	}

	env := []string{}
	if pass != "" {
		env = append(env, "PGPASSWORD="+pass)
	}
	_, err = ContainerExecWithEnv(container, env,
		"psql", "-U", user, "-d", db,
		"-c", `DO $$ DECLARE r RECORD; BEGIN FOR r IN (SELECT tablename FROM pg_tables WHERE schemaname='public') LOOP EXECUTE 'TRUNCATE TABLE ' || quote_ident(r.tablename) || ' CASCADE'; END LOOP; END $$;`,
	)
	if err != nil {
		return fmt.Errorf("truncate tables: %w", err)
	}
	return nil
}

// MySqlTruncate truncates all tables in a MySQL container.
func MySqlTruncate(profileName, envName, serviceName string) error {
	container := ContainerName(profileName, envName, serviceName)

	user, err := ContainerEnv(container, "MYSQL_ROOT_USER")
	if err != nil {
		user, err = ContainerEnv(container, "MYSQL_USER")
		if err != nil {
			return fmt.Errorf("read MYSQL_USER: %w", err)
		}
	}
	pass, _ := ContainerEnv(container, "MYSQL_ROOT_PASSWORD")
	if pass == "" {
		pass, _ = ContainerEnv(container, "MYSQL_PASSWORD")
	}
	db, err := ContainerEnv(container, "MYSQL_DATABASE")
	if err != nil {
		return fmt.Errorf("read MYSQL_DATABASE: %w", err)
	}

	_, err = ContainerExec(container, "sh", "-c",
		fmt.Sprintf(`mysql -u %s -p'%s' %s -e "
			SET FOREIGN_KEY_CHECKS=0;
			SELECT GROUP_CONCAT(CONCAT('TRUNCATE TABLE ', table_name, ';') SEPARATOR ' ')
			INTO @sql FROM information_schema.tables WHERE table_schema = '%s';
			PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;
		"`, user, pass, db, db),
	)
	if err != nil {
		return fmt.Errorf("truncate tables: %w", err)
	}
	return nil
}

// MongoTruncate drops all non-system collections in the database via docker exec.
func MongoTruncate(profileName, envName, serviceName string) error {
	container := ContainerName(profileName, envName, serviceName)
	_, err := ContainerExec(container, "mongosh", "--eval",
		`db.getCollectionNames().forEach(function(c) {
			if (c.indexOf("system.") == -1) db.getCollection(c).deleteMany({});
		})`,
	)
	if err != nil {
		return fmt.Errorf("truncate collections: %w", err)
	}
	return nil
}

// MsSqlTruncate truncates all tables in the default schema via docker exec.
func MsSqlTruncate(profileName, envName, serviceName string) error {
	container := ContainerName(profileName, envName, serviceName)

	saPass, err := ContainerEnv(container, "MSSQL_SA_PASSWORD")
	if err != nil {
		return fmt.Errorf("read MSSQL_SA_PASSWORD: %w", err)
	}

	_, err = ContainerExec(container, "sh", "-c",
		fmt.Sprintf(`/opt/mssql-tools/bin/sqlcmd -S localhost -U sa -P '%s' -d master -Q "EXEC sp_MSforeachtable 'TRUNCATE TABLE ?'"`, saPass),
	)
	if err != nil {
		return fmt.Errorf("truncate tables: %w", err)
	}
	return nil
}

// SqlPackageExport exports an MSSQL database to a .bacpac file using sqlpackage CLI.
func SqlPackageExport(syncEnv *config.SyncEnv, outPath string) error {
	if !ToolAvailable("sqlpackage") {
		return fmt.Errorf("sqlpackage CLI not found. Install via: dotnet tool install -g microsoft.sqlpackage")
	}
	args := []string{
		"/Action:Export",
		"/SourceServerName:" + syncEnv.Host + "," + syncEnv.Port,
		"/SourceDatabaseName:" + syncEnv.Database,
		"/SourceUser:" + syncEnv.User,
		"/SourcePassword:" + syncEnv.Password,
		"/TargetFile:" + outPath,
	}
	return exec.Command("sqlpackage", args...).Run()
}

// SqlPackageImport imports a .bacpac file into the MSSQL container using sqlpackage CLI.
// sqlpackage runs on the HOST targeting localhost:mapped_port.
// The SA password is read from the running container's MSSQL_SA_PASSWORD env var.
func SqlPackageImport(profileName, envName, serviceName string, inPath string, hostPort string) error {
	if !ToolAvailable("sqlpackage") {
		return fmt.Errorf("sqlpackage CLI not found. Install via: dotnet tool install -g microsoft.sqlpackage")
	}
	container := ContainerName(profileName, envName, serviceName)
	saPass, err := ContainerEnv(container, "MSSQL_SA_PASSWORD")
	if err != nil {
		return fmt.Errorf("read MSSQL_SA_PASSWORD from container: %w", err)
	}
	args := []string{
		"/Action:Import",
		"/TargetServerName:localhost," + hostPort,
		"/TargetDatabaseName:master",
		"/TargetUser:sa",
		"/TargetPassword:" + saPass,
		"/SourceFile:" + inPath,
	}
	return exec.Command("sqlpackage", args...).Run()
}
