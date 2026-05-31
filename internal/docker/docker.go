package docker

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
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
	return exec.Command("docker", composeArgs(profileName, envName, "up", "-d")...).Run()
}

func UpService(profileName, envName, serviceName string) error {
	return exec.Command("docker", composeArgs(profileName, envName, "up", "-d", serviceName)...).Run()
}

func Down(profileName, envName string) error {
	return exec.Command("docker", composeArgs(profileName, envName, "down")...).Run()
}

func DownService(profileName, envName, serviceName string) error {
	return exec.Command("docker", composeArgs(profileName, envName, "stop", serviceName)...).Run()
}

func DownVolumes(profileName, envName string) error {
	return exec.Command("docker", composeArgs(profileName, envName, "down", "-v")...).Run()
}

func Restart(profileName, envName, service string) error {
	return exec.Command("docker", composeArgs(profileName, envName, "restart", service)...).Run()
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
	return err
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
func PgDump(pg *config.PostgresOps, outPath, flag string) error {
	args := []string{"-h", pg.Host, "-U", pg.User}
	if flag != "" {
		args = append(args, flag)
	}
	args = append(args, pg.Database)
	cmd := exec.Command("pg_dump", args...)
	cmd.Env = append(os.Environ(), "PGPASSWORD="+pg.Password)
	out, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("pg_dump: %w", err)
	}
	return os.WriteFile(outPath, out, 0644)
}

// PgRestore restores a dump file via psql.
func PgRestore(pg *config.PostgresOps, inPath string) error {
	args := []string{"-h", pg.Host, "-U", pg.User, "-d", pg.Database, "-f", inPath}
	cmd := exec.Command("psql", args...)
	cmd.Env = append(os.Environ(), "PGPASSWORD="+pg.Password)
	return cmd.Run()
}

// MongoDump runs mongodump.
func MongoDump(m *config.MongoOps, outDir string) error {
	return exec.Command("mongodump",
		"--host", m.Host+":"+m.Port,
		"--username", m.User,
		"--password", m.Password,
		"--authenticationDatabase", m.AuthDB,
		"--db", m.Database,
		"--out", outDir,
	).Run()
}

// MySqlDump runs mysqldump.
func MySqlDump(m *config.MySqlOps, outPath string) error {
	args := []string{"-h", m.Host, "-P", m.Port, "-u", m.User, m.Database}
	cmd := exec.Command("mysqldump", args...)
	cmd.Env = append(os.Environ(), "MYSQL_PWD="+m.Password)
	out, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("mysqldump: %w", err)
	}
	return os.WriteFile(outPath, out, 0644)
}

// MySqlRestore restores a mysql dump via mysql client.
func MySqlRestore(m *config.MySqlOps, inPath string) error {
	args := []string{"-h", m.Host, "-P", m.Port, "-u", m.User, m.Database, "-e", "source " + inPath}
	cmd := exec.Command("mysql", args...)
	cmd.Env = append(os.Environ(), "MYSQL_PWD="+m.Password)
	return cmd.Run()
}

// MongoRestore runs mongorestore with --drop.
func MongoRestore(m *config.MongoOps, inDir string) error {
	return exec.Command("mongorestore",
		"--host", m.Host+":"+m.Port,
		"--username", m.User,
		"--password", m.Password,
		"--authenticationDatabase", m.AuthDB,
		"--db", m.Database,
		"--drop",
		filepath.Join(inDir, m.Database),
	).Run()
}
