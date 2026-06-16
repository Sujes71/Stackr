package docker

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/stackr/stackr/internal/config"
)

// dockerRunUserID is no longer needed — Docker only chowns mount sources
// when it CREATES them. If the directory exists beforehand, no chown occurs.
// (Colima's sshfs and Docker Desktop's osxfs both reject container-user chowns.)

func composePath(profileName, envName string) string {
	return filepath.Join(config.ProfileDir(profileName), envName, "docker-compose.yml")
}

func composeArgs(profileName, envName string, args ...string) []string {
	base := []string{"compose", "-f", composePath(profileName, envName)}
	return append(base, args...)
}

// DefaultToolImage returns a sensible default Docker image for the given DB type.
// This is only a fallback — the actual ToolImage comes from the SyncEnv config
// (pre-filled from the service image in the Wizard), and the user can override it.
func DefaultToolImage(dbType string) string {
	switch dbType {
	case "postgres":
		return "postgres:17"
	case "mongo":
		return "mongo:7.0"
	case "mysql":
		return "mysql:9.0"
	case "mssql":
		return "mcr.microsoft.com/mssql-tools:latest"
	default:
		return ""
	}
}

// ToolImage returns the tool image for a SyncEnv, or the default if not set.
func ToolImage(se *config.SyncEnv) string {
	if se.ToolImage != "" {
		return se.ToolImage
	}
	return DefaultToolImage(se.DBType)
}

// ================================================================
// Queue tool images
// ================================================================

// DefaultQueueToolImage returns the default tool image for the given queue type.
func DefaultQueueToolImage(queueType string) string {
	switch queueType {
	case "kafka":
		return "confluentinc/cp-kafka:latest"
	case "rabbitmq":
		return "rabbitmq:3-management"
	case "activemq":
		return "apache/activemq-classic:latest"
	default:
		return ""
	}
}

// QueueToolImage returns the tool image for a QueueSyncEnv, or the default if not set.
func QueueToolImage(qse *config.QueueSyncEnv) string {
	if qse.ToolImage != "" {
		return qse.ToolImage
	}
	return DefaultQueueToolImage(qse.QueueType)
}

// DaemonAvailable checks if the Docker daemon is reachable by running
// `docker version --format '{{.Server.Version}}'` with a timeout.
func DaemonAvailable() bool {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "docker", "version", "--format", "{{.Server.Version}}")
	if err := cmd.Run(); err != nil {
		return false
	}
	return true
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
		return []ServiceStatus{}, friendlyError(err, string(out))
	}
	var statuses []ServiceStatus
	scanner := bufio.NewScanner(strings.NewReader(string(out)))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var raw map[string]interface{}
		if err := json.Unmarshal([]byte(line), &raw); err != nil {
			continue
		}
		statuses = append(statuses, ServiceStatus{
			Name:   jsonString(raw, "Name"),
			State:  jsonString(raw, "State"),
			Ports:  jsonString(raw, "Publishers"),
			Health: jsonString(raw, "Health"),
		})
	}
	return statuses, nil
}

func jsonString(m map[string]interface{}, key string) string {
	if v, ok := m[key]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
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

func StopService(profileName, envName, serviceName string) error {
	return runDocker(composeArgs(profileName, envName, "stop", serviceName)...)
}

// Deprecated: Use StopService instead (which calls docker compose stop).
// DownService only calls docker compose stop despite the misleading name.
func DownService(profileName, envName, serviceName string) error {
	return StopService(profileName, envName, serviceName)
}

func RmService(profileName, envName, serviceName string) error {
	return runDocker(composeArgs(profileName, envName, "rm", "-fs", serviceName)...)
}

// ResetServiceVolumes stops the service, removes its container + anonymous volumes,
// then starts it again. Named volumes are not affected.
func ResetServiceVolumes(profileName, envName, serviceName string) error {
	if err := StopService(profileName, envName, serviceName); err != nil {
		return fmt.Errorf("stop: %w", err)
	}
	// rm -fsv: force stop + remove container + remove anonymous volumes
	if err := runDocker(composeArgs(profileName, envName, "rm", "-fsv", serviceName)...); err != nil {
		// Ignore errors if the container is already gone
	}
	return UpService(profileName, envName, serviceName)
}

func DownVolumes(profileName, envName string) error {
	return runDocker(composeArgs(profileName, envName, "down", "-v")...)
}

func ComposePull(profileName, envName, service string) error {
	return runDocker(composeArgs(profileName, envName, "pull", service)...)
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
	pass, err := syncEnv.GetPassword()
	if err != nil {
		return fmt.Errorf("decrypt password: %w", err)
	}

	outDir := filepath.Dir(outPath)
	outFile := filepath.Base(outPath)

	dockerArgs := []string{
		"run", "--rm",
		"-v", outDir + ":/dump",
		"-e", "PGPASSWORD=" + pass,
		ToolImage(syncEnv),
		"pg_dump",
		"-h", syncEnv.Host,
		"-p", syncEnv.Port,
		"-U", syncEnv.User,
		"-f", "/dump/" + outFile,
	}
	if flag != "" {
		dockerArgs = append(dockerArgs, flag)
	}
	dockerArgs = append(dockerArgs, syncEnv.Database)

	out, err := exec.Command("docker", dockerArgs...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("pg_dump via docker: %s: %w", strings.TrimSpace(string(out)), err)
	}
	return nil
}

// MongoDump runs mongodump.
func MongoDump(syncEnv *config.SyncEnv, outDir string) error {
	pass, err := syncEnv.GetPassword()
	if err != nil {
		return fmt.Errorf("decrypt password: %w", err)
	}

	// Pre-create the output dir so Docker doesn't try to chown it.
	if err := os.MkdirAll(outDir, 0755); err != nil {
		return fmt.Errorf("create dump dir: %w", err)
	}

	dockerArgs := []string{
		"run", "--rm",
		"-v", outDir + ":/dump",
		ToolImage(syncEnv),
		"mongodump",
		"--host", syncEnv.Host,
		"--port", syncEnv.Port,
		"--username", syncEnv.User,
		"--password", pass,
	}
	if syncEnv.AuthDB != "" {
		dockerArgs = append(dockerArgs, "--authenticationDatabase", syncEnv.AuthDB)
	}
	dockerArgs = append(dockerArgs,
		"--db", syncEnv.Database,
		"--out", "/dump",
	)

	out, err := exec.Command("docker", dockerArgs...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("mongodump via docker: %s: %w", strings.TrimSpace(string(out)), err)
	}
	return nil
}

// MySqlDump runs mysqldump.
func MySqlDump(syncEnv *config.SyncEnv, outPath string) error {
	pass, err := syncEnv.GetPassword()
	if err != nil {
		return fmt.Errorf("decrypt password: %w", err)
	}

	outDir := filepath.Dir(outPath)
	outFile := filepath.Base(outPath)

	dockerArgs := []string{
		"run", "--rm",
		"-v", outDir + ":/dump",
		"-e", "MYSQL_PWD=" + pass,
		ToolImage(syncEnv),
		"mysqldump",
		"-h", syncEnv.Host,
		"-P", syncEnv.Port,
		"-u", syncEnv.User,
		"-r", "/dump/" + outFile,
		syncEnv.Database,
	}

	out, err := exec.Command("docker", dockerArgs...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("mysqldump via docker: %s: %w", strings.TrimSpace(string(out)), err)
	}
	return nil
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

// ContainerCopyFrom copies a file or directory FROM the container to the host.
// docker cp <container>:<src> <dest>
func ContainerCopyFrom(container, srcPath, destPath string) error {
	return exec.Command("docker", "cp", container+":"+srcPath, destPath).Run()
}

// ContainerExec runs an arbitrary command inside the container.
func ContainerExec(container string, args ...string) (string, error) {
	cmd := exec.Command("docker", append([]string{"exec", container}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("docker exec: %s: %w", strings.TrimSpace(string(out)), err)
	}
	return string(out), nil
}

// ExecShell returns a command to open an interactive shell (bash or sh) in the container.
// The command is NOT executed — the caller must run it or pass it to tea.Exec.
// Stdio is connected to the terminal so the shell is fully interactive.
func ExecShell(containerName string) (*exec.Cmd, error) {
	if _, err := exec.LookPath("docker"); err != nil {
		return nil, fmt.Errorf("docker not available")
	}

	shell := "sh"
	checkCmd := exec.Command("docker", "exec", containerName, "which", "bash")
	if err := checkCmd.Run(); err == nil {
		shell = "bash"
	}

	cmd := exec.Command("docker", "exec", "-it", containerName, shell)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd, nil
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
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("docker exec: %s: %w", strings.TrimSpace(string(out)), err)
	}
	return string(out), nil
}

// ================================================================
// Queue container helpers
// ================================================================

// QueueExecLocal executes a queue command inside the running container via docker exec.
// containerName: the service container name
// program: the CLI tool binary (e.g., "kafka-topics", "rabbitmqadmin")
// args: arguments passed to the tool
// Returns combined stdout+stderr and any error.
//
// For ActiveMQ commands, auto-detects the JMX RMI port if no -Dactivemq.jmx.url
// flag is already present in args, and prepends the correct JMX JVM flag.
func QueueExecLocal(containerName, program string, args ...string) (string, error) {
	cmdArgs := []string{"exec", containerName}
	if program == "activemq" {
		// Bypass the activemq shell script and call the JAR directly.
		//
		// The activemq shell script does:
		//   COMMANDLINE_ARGS="$@"   (line 49 — joins all args with spaces)
		//   java ... $COMMANDLINE_ARGS  (line 362 — unquoted, so IFS splits
		//   arg values containing spaces!)
		//
		// This means --message "hello world" arrives as two separate args
		// ("hello", "world"), --messageCount and its value become positional
		// noise, and the default 1000 messages is used instead of 1.
		//
		// By calling java -jar directly, each argv element stays intact and
		// proper quoting is preserved.
		port := detectActiveMQJmxPort(containerName)
		activemqOpts := fmt.Sprintf(
			"ACTIVEMQ_OPTS=-Dcom.sun.management.jmxremote.ssl=false -Dactivemq.jmx.url=service:jmx:rmi:///jndi/rmi://localhost:%s/jmxrmi",
			port,
		)
		jarArgs := append([]string{"-jar", "/opt/apache-activemq/bin/activemq.jar"}, args...)
		cmdArgs = []string{"exec", "-e", activemqOpts, containerName, "java"}
		cmdArgs = append(cmdArgs, jarArgs...)
	} else {
		cmdArgs = append(cmdArgs, program)
		cmdArgs = append(cmdArgs, args...)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "docker", cmdArgs...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("%s: %s: %w", program, strings.TrimSpace(string(out)), err)
	}
	return string(out), nil
}

// detectActiveMQJmxPort returns the JMX RMI registry port for an ActiveMQ container.
// The standard JMX RMI registry port is 1099 — we confirm it's listening in
// /proc/1/net/tcp before returning, falling back to 1099 as the default.
func detectActiveMQJmxPort(containerName string) string {
	portsCmd := `cat /proc/1/net/tcp 2>/dev/null | awk 'NR>1 {print $2}' | cut -d: -f2 | while read h; do printf "%d\n" 0x$h 2>/dev/null; done | sort -nu`
	out, err := exec.Command("docker", "exec", containerName, "sh", "-c", portsCmd).Output()
	if err != nil {
		return "1099"
	}
	for _, f := range strings.Fields(string(out)) {
		if f == "1099" {
			return "1099"
		}
	}
	// Port 1099 is the standard JMX RMI registry — use it even if not
	// detected (e.g., container started without ACTIVEMQ_OPTS).
	return "1099"
}

// QueueExecRemote executes a queue command against a remote QueueSyncEnv via
// a throwaway container (docker run --rm) using the configured tool image.
// qse: the QueueSyncEnv with connection details
// program: the CLI tool binary
// args: arguments (should include host/port connectivity flags)
func QueueExecRemote(qse *config.QueueSyncEnv, program string, args ...string) (string, error) {
	image := QueueToolImage(qse)
	cmdArgs := append([]string{"run", "--rm", image, program}, args...)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "docker", cmdArgs...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("%s: %s: %w", program, strings.TrimSpace(string(out)), err)
	}
	return string(out), nil
}

// QueueContainerRunning checks whether a queue container exists and is running
// via docker inspect. Returns (false, nil) if the container doesn't exist or
// the Docker daemon is not available.
func QueueContainerRunning(containerName string) (bool, error) {
	cmd := exec.Command("docker", "inspect", "-f", "{{.State.Running}}", containerName)
	out, err := cmd.Output()
	if err != nil {
		return false, nil
	}
	return strings.TrimSpace(string(out)) == "true", nil
}

// ContainerReady checks if the database inside the container is accepting connections.
func ContainerReady(container, dbType string) bool {
	switch dbType {
	case "postgres":
		return exec.Command("docker", "exec", container, "pg_isready", "-q").Run() == nil
	case "mongo":
		// Read env vars using `sh -c 'echo $VAR'` instead of `printenv`
		// so that undefined variables return empty string without error.
		readEnv := func(envVar string) string {
			out, err := exec.Command("docker", "exec", container, "sh", "-c",
				fmt.Sprintf("echo ${%s:-}", envVar)).Output()
			if err != nil {
				return ""
			}
			return strings.TrimSpace(string(out))
		}
		user := readEnv("MONGO_INITDB_ROOT_USERNAME")
		pass := readEnv("MONGO_INITDB_ROOT_PASSWORD")

		// Try mongosh first, fall back to mongo
		shell := "mongosh"
		if _, err := ContainerExec(container, "which", "mongosh"); err != nil {
			shell = "mongo"
		}
		args := []string{shell, "--quiet"}
		if user != "" {
			args = append(args, "--username", user)
			if pass != "" {
				args = append(args, "--password", pass)
			}
			args = append(args, "--authenticationDatabase", "admin")
		}
		args = append(args, "--eval", "1")
		return exec.Command("docker", append([]string{"exec", container}, args...)...).Run() == nil
	case "mysql":
		return exec.Command("docker", "exec", container, "mysqladmin", "ping", "--silent").Run() == nil
	case "mssql":
		saPass, err := ContainerEnv(container, "MSSQL_SA_PASSWORD")
		if err != nil {
			return false
		}
		sqlcmd := "/opt/mssql-tools18/bin/sqlcmd"
		if _, err := ContainerExec(container, "test", "-x", sqlcmd); err != nil {
			sqlcmd = "/opt/mssql-tools/bin/sqlcmd"
		}
		_, err = ContainerExec(container, sqlcmd,
			"-S", "localhost",
			"-U", "sa",
			"-P", saPass,
			"-Q", "SELECT 1",
			"-l", "2", // 2-second timeout
		)
		return err == nil
	case "oracle":
		user, pass, pdb := oracleCreds(container)
		err := exec.Command("docker", "exec", container,
			"bash", "-c",
			fmt.Sprintf("echo \"SELECT 'ready' FROM dual;\" | sqlplus -S %s/%s@//localhost:1521/%s", user, pass, pdb),
		).Run()
		return err == nil
	default:
		return true // unknown type — assume ready
	}
}

// MockServerReady checks if MockServer is accepting HTTP requests.
// The container image is a minimal Java runtime with no shell tools (sh/bash/nc),
// so we must check via HTTP from the host.
// We use PUT /mockserver/retrieve which is a known working endpoint.
func MockServerReady(hostPort string) bool {
	client := &http.Client{Timeout: 2 * time.Second}
	req, err := http.NewRequest("PUT", "http://localhost:"+hostPort+"/mockserver/retrieve?type=ACTIVE_EXPECTATIONS", nil)
	if err != nil {
		return false
	}
	resp, err := client.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode == http.StatusOK
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
		var dirs []string
		for _, e := range entries {
			if e.IsDir() {
				dirs = append(dirs, filepath.Join(dumpDir, e.Name()))
			}
		}
		if len(dirs) == 0 {
			return "", fmt.Errorf("no dump directories found in %s", dumpDir)
		}
		sort.Slice(dirs, func(i, j int) bool {
			fi, _ := os.Stat(dirs[i])
			fj, _ := os.Stat(dirs[j])
			if fi == nil || fj == nil {
				return false
			}
			return fi.ModTime().After(fj.ModTime())
		})
		return dirs[0], nil
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

	user, _ := ContainerEnv(container, "MONGO_INITDB_ROOT_USERNAME")
	pass, _ := ContainerEnv(container, "MONGO_INITDB_ROOT_PASSWORD")

	tmpDir := "/tmp/" + filepath.Base(dumpDir)
	if err := ContainerCopy(container, dumpDir, tmpDir); err != nil {
		return fmt.Errorf("docker cp: %w", err)
	}

	args := []string{"mongorestore", "--drop", "--dir", tmpDir}
	if user != "" {
		args = append(args, "--username", user)
		if pass != "" {
			args = append(args, "--password", pass)
		}
		args = append(args, "--authenticationDatabase", "admin")
	}

	_, err := ContainerExec(container, args...)
	if err != nil {
		return fmt.Errorf("mongorestore: %w", err)
	}
	return nil
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

	env := []string{}
	if pass != "" {
		env = append(env, "MYSQL_PWD="+pass)
	}
	_, err = ContainerExecWithEnv(container, env,
		"sh", "-c", fmt.Sprintf("mysql -u %s %s < %s", user, db, tmpPath))
	return err
}

// ================================================================
// SqlPackage helpers (MSSQL — runs on host, targets container port)
// ================================================================

// ================================================================
// Truncate functions (docker exec based)
// ================================================================

// PgTruncate truncates all user tables across every non-system schema.
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
		"-c", `DO $$ DECLARE r RECORD; BEGIN FOR r IN (SELECT schemaname, tablename FROM pg_tables WHERE schemaname NOT IN ('pg_catalog', 'information_schema')) LOOP EXECUTE 'TRUNCATE TABLE ' || quote_ident(r.schemaname) || '.' || quote_ident(r.tablename) || ' CASCADE'; END LOOP; END $$;`,
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

	env := []string{}
	if pass != "" {
		env = append(env, "MYSQL_PWD="+pass)
	}
	_, err = ContainerExecWithEnv(container, env, "sh", "-c",
		fmt.Sprintf(`mysql -u %s %s -e "
			SET FOREIGN_KEY_CHECKS=0;
			SELECT GROUP_CONCAT(CONCAT('TRUNCATE TABLE ', table_name, ';') SEPARATOR ' ')
			INTO @sql FROM information_schema.tables WHERE table_schema = '%s';
			PREPARE stmt FROM @sql; EXECUTE stmt; DEALLOCATE PREPARE stmt;
		"`, user, db, db),
	)
	if err != nil {
		return fmt.Errorf("truncate tables: %w", err)
	}
	return nil
}

// MongoTruncate drops all non-system collections via docker exec.
func MongoTruncate(profileName, envName, serviceName string) error {
	container := ContainerName(profileName, envName, serviceName)

	user, _ := ContainerEnv(container, "MONGO_INITDB_ROOT_USERNAME")
	pass, _ := ContainerEnv(container, "MONGO_INITDB_ROOT_PASSWORD")

	// Read target database from container env; fall back to "test" (MongoDB default).
	database, _ := ContainerEnv(container, "MONGO_INITDB_DATABASE")
	if database == "" {
		database = "test"
	}

	// Try mongosh first (MongoDB 6+), fall back to mongo (5.x and older).
	shell := "mongosh"
	if _, err := ContainerExec(container, "which", "mongosh"); err != nil {
		if _, err2 := ContainerExec(container, "which", "mongo"); err2 != nil {
			return fmt.Errorf("neither mongosh nor mongo found in container")
		}
		shell = "mongo"
	}

	mongoArgs := []string{shell}
	if user != "" {
		mongoArgs = append(mongoArgs, "--username", user)
		if pass != "" {
			mongoArgs = append(mongoArgs, "--password", pass)
		}
		mongoArgs = append(mongoArgs, "--authenticationDatabase", "admin")
	}
	mongoArgs = append(mongoArgs, database, "--eval",
		`db.getCollectionNames().forEach(function(c) {
			if (c.indexOf("system.") == -1) db.getCollection(c).deleteMany({});
		})`,
	)

	_, err := ContainerExec(container, mongoArgs...)
	if err != nil {
		return fmt.Errorf("truncate collections: %w", err)
	}
	return nil
}

// MsSqlTruncate truncates all tables in the database via docker exec.
func MsSqlTruncate(profileName, envName, serviceName, database string) error {
	container := ContainerName(profileName, envName, serviceName)

	saPass, err := ContainerEnv(container, "MSSQL_SA_PASSWORD")
	if err != nil {
		return fmt.Errorf("read MSSQL_SA_PASSWORD: %w", err)
	}

	// Detect sqlcmd path: mssql-tools18 (newer images) vs mssql-tools (older).
	sqlcmd := "/opt/mssql-tools18/bin/sqlcmd"
	if _, err := ContainerExec(container, "test", "-x", sqlcmd); err != nil {
		sqlcmd = "/opt/mssql-tools/bin/sqlcmd"
	}

	_, err = ContainerExec(container, sqlcmd,
		"-S", "localhost",
		"-U", "sa",
		"-P", saPass,
		"-d", database,
		"-Q", fmt.Sprintf("EXEC sp_MSforeachtable 'TRUNCATE TABLE ?'"),
	)
	if err != nil {
		return fmt.Errorf("truncate tables: %w", err)
	}
	return nil
}

// SqlPackageExport exports an MSSQL database to a .bacpac file using sqlpackage CLI.
func SqlPackageExport(syncEnv *config.SyncEnv, outPath string) error {
	pass, err := syncEnv.GetPassword()
	if err != nil {
		return fmt.Errorf("decrypt password: %w", err)
	}

	outDir := filepath.Dir(outPath)
	outFile := filepath.Base(outPath)

	dockerArgs := []string{
		"run", "--rm",
		"-v", outDir + ":/tmp",
		ToolImage(syncEnv),
		"sqlpackage",
		"/Action:Export",
		"/SourceServerName:" + syncEnv.Host + "," + syncEnv.Port,
		"/SourceDatabaseName:" + syncEnv.Database,
		"/SourceUser:" + syncEnv.User,
		"/SourcePassword:" + pass,
		"/TargetFile:/tmp/" + outFile,
	}

	out, err := exec.Command("docker", dockerArgs...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("sqlpackage via docker: %s: %w", strings.TrimSpace(string(out)), err)
	}
	return nil
}

// OracleTruncate truncates all user tables in an Oracle container using sqlplus.
func OracleTruncate(profileName, envName, serviceName string) error {
	container := ContainerName(profileName, envName, serviceName)
	user, pass, pdb := oracleCreds(container)
	// PL/SQL block: truncate every table visible to the connecting user
	sql := "BEGIN FOR t IN (SELECT table_name FROM user_tables) LOOP EXECUTE IMMEDIATE 'TRUNCATE TABLE ' || t.table_name; END LOOP; END;"
	_, err := ContainerExec(container, "sh", "-c",
		fmt.Sprintf("echo \"%s\" | sqlplus -S %s/%s@//localhost:1521/%s", sql, user, pass, pdb))
	if err != nil {
		return fmt.Errorf("truncate oracle: %w", err)
	}
	return nil
}

// OracleExport exports data from a local Oracle container using Data Pump (expdp).
// The dump file is created inside the container (server-side) then copied out to outPath.
// Remote Oracle export is not supported via this function — use Data Pump manually.
func OracleExport(profileName, envName, serviceName, outPath string) error {
	container := ContainerName(profileName, envName, serviceName)
	user, pass, pdb := oracleCreds(container)
	outFile := filepath.Base(outPath)
	outDir := filepath.Dir(outPath)
	containerDumpDir := "/tmp/stackr_dump"

	// Ensure local dump dir exists
	if err := os.MkdirAll(outDir, 0755); err != nil {
		return fmt.Errorf("create dump dir: %w", err)
	}

	// 1. Create temp dir inside the container
	ContainerExec(container, "mkdir", "-p", containerDumpDir) //nolint:errcheck

	// 2. Create Oracle directory object
	dirSQL := fmt.Sprintf("CREATE OR REPLACE DIRECTORY STACKR_DUMP AS '%s'", containerDumpDir)
	if _, err := ContainerExec(container, "bash", "-c",
		fmt.Sprintf(`echo "%s" | sqlplus -S %s/%s@//localhost:1521/%s`, dirSQL, user, pass, pdb)); err != nil {
		ContainerExec(container, "rm", "-rf", containerDumpDir)
		return fmt.Errorf("create directory object: %w", err)
	}

	// 3. Run expdp inside the container
	if out, err := ContainerExec(container, "bash", "-c",
		fmt.Sprintf(`expdp %s/%s@//localhost:1521/%s directory=STACKR_DUMP dumpfile=%s schemas=%s`,
			user, pass, pdb, outFile, user)); err != nil {
		ContainerExec(container, "rm", "-rf", containerDumpDir)
		return fmt.Errorf("oracle export: %s: %w", strings.TrimSpace(out), err)
	}

	// 4. Copy dump file out of the container
	if err := ContainerCopyFrom(container, filepath.Join(containerDumpDir, outFile), outPath); err != nil {
		ContainerExec(container, "rm", "-rf", containerDumpDir)
		return fmt.Errorf("copy dump from container: %w", err)
	}

	// 5. Clean up (best-effort)
	ContainerExec(container, "rm", "-rf", containerDumpDir) //nolint:errcheck
	return nil
}

// OracleImport imports a Data Pump dump file into a local Oracle container.
// The file is copied into the container first, then impdp restores it.
// Remote Oracle import is not supported via this function — use Data Pump manually.
func OracleImport(profileName, envName, serviceName, inPath string) error {
	container := ContainerName(profileName, envName, serviceName)
	user, pass, pdb := oracleCreds(container)
	containerDumpDir := "/tmp/stackr_dump"
	inFile := filepath.Base(inPath)

	// 1. Create temp dir inside the container
	ContainerExec(container, "mkdir", "-p", containerDumpDir) //nolint:errcheck

	// 2. Create Oracle directory object pointing to the temp dir
	dirSQL := fmt.Sprintf("CREATE OR REPLACE DIRECTORY STACKR_DUMP AS '%s'", containerDumpDir)
	_, err := ContainerExec(container, "bash", "-c",
		fmt.Sprintf(`echo "%s" | sqlplus -S %s/%s@//localhost:1521/%s`, dirSQL, user, pass, pdb))
	if err != nil {
		ContainerExec(container, "rm", "-rf", containerDumpDir)
		return fmt.Errorf("create directory object: %w", err)
	}

	// 3. Copy dump file into the container
	if err := ContainerCopy(container, inPath, filepath.Join(containerDumpDir, inFile)); err != nil {
		ContainerExec(container, "rm", "-rf", containerDumpDir)
		return fmt.Errorf("copy dump into container: %w", err)
	}

	// 4. Run impdp (TABLE_EXISTS_ACTION=REPLACE handles re-imports)
	impdpOut, err := ContainerExec(container, "bash", "-c",
		fmt.Sprintf(`impdp %s/%s@//localhost:1521/%s directory=STACKR_DUMP dumpfile=%s table_exists_action=replace`,
			user, pass, pdb, inFile))
	if err != nil {
		ContainerExec(container, "rm", "-rf", containerDumpDir)
		return fmt.Errorf("oracle import: %s: %w", strings.TrimSpace(impdpOut), err)
	}

	// 5. Clean up (best-effort)
	ContainerExec(container, "rm", "-rf", containerDumpDir) //nolint:errcheck

	return nil
}

// oracleCreds reads Oracle connection credentials from the running container.
func oracleCreds(container string) (user, pass, pdb string) {
	user, _ = ContainerEnv(container, "ORACLE_USER")
	if user == "" {
		user = "system"
	}
	pass, _ = ContainerEnv(container, "ORACLE_PASSWORD")
	if pass == "" {
		pass = "oracle"
	}
	pdb, _ = ContainerEnv(container, "ORACLE_PDB")
	if pdb == "" {
		pdb = "FREEPDB1"
	}
	return
}

// SqlPackageImport imports a .bacpac file into the MSSQL container using sqlpackage CLI.
func SqlPackageImport(profileName, envName, serviceName, inPath, hostPort, toolImage string) error {
	container := ContainerName(profileName, envName, serviceName)
	saPass, err := ContainerEnv(container, "MSSQL_SA_PASSWORD")
	if err != nil {
		return fmt.Errorf("read MSSQL_SA_PASSWORD from container: %w", err)
	}

	if toolImage == "" {
		toolImage = DefaultToolImage("mssql")
	}

	inDir := filepath.Dir(inPath)
	inFile := filepath.Base(inPath)

	dockerArgs := []string{
		"run", "--rm",
		"--add-host", "host.docker.internal:host-gateway",
		"-v", inDir + ":/tmp",
		toolImage,
		"sqlpackage",
		"/Action:Import",
		"/TargetServerName:host.docker.internal," + hostPort,
		"/TargetDatabaseName:master",
		"/TargetUser:sa",
		"/TargetPassword:" + saPass,
		"/SourceFile:/tmp/" + inFile,
	}

	out, err := exec.Command("docker", dockerArgs...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("sqlpackage via docker: %s: %w", strings.TrimSpace(string(out)), err)
	}
	return nil
}
