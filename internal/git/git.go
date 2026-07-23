// Package git provides a thin CLI wrapper for git operations.
// All functions take dir as the first argument — the working directory
// (typically the profile directory). Commands run via
// exec.Command("git", "-C", dir, ...args...).
package git

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// ── Data Types ────────────────────────────────────────────────────────────────

// FileStatus represents a single file's git status.
type FileStatus struct {
	Path       string // relative file path
	StatusCode byte   // first column: M, A, D, ?, C, U (index/staged)
	IndexCode  byte   // second column: M, D, C (worktree/unstaged)
	Staged     bool   // true if file has staged changes
}

// CommitInfo represents a single log entry.
type CommitInfo struct {
	Hash    string // abbreviated hash
	Author  string
	Date    string // relative or formatted date
	Message string
}

// BranchInfo represents a local branch.
type BranchInfo struct {
	Name     string
	Current  bool   // * marker
	Tracking string // upstream tracking info
}

// StashInfo represents a stash entry.
type StashInfo struct {
	Ref     string // e.g. "stash@{0}"
	Message string
}

// RemoteInfo represents a git remote.
type RemoteInfo struct {
	Name string
	URL  string
}

// TagInfo represents a git tag.
type TagInfo struct {
	Name       string
	Annotation string // empty for lightweight tags
}

// ── Core Runner ───────────────────────────────────────────────────────────────

const defaultTimeout = 30 * time.Second

// run executes a git command in the given directory and returns combined output.
func run(dir string, args ...string) (string, error) {
	return runWithTimeout(dir, defaultTimeout, args...)
}

func runWithTimeout(dir string, timeout time.Duration, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		errMsg := strings.TrimSpace(stderr.String())
		if errMsg == "" {
			errMsg = err.Error()
		}
		return "", fmt.Errorf("git %s: %s", strings.Join(args, " "), errMsg)
	}
	return stdout.String(), nil
}

// ── Repo Info ─────────────────────────────────────────────────────────────────

// IsRepo checks if dir is inside a git work tree.
func IsRepo(dir string) bool {
	_, err := run(dir, "rev-parse", "--is-inside-work-tree")
	return err == nil
}

// Root returns the git root for dir, or error if not a repo.
func Root(dir string) (string, error) {
	out, err := run(dir, "rev-parse", "--show-toplevel")
	return strings.TrimSpace(out), err
}

// ── Status ────────────────────────────────────────────────────────────────────

// Status runs `git status --porcelain` and parses into FileStatus slice.
func Status(dir string) ([]FileStatus, error) {
	out, err := run(dir, "status", "--porcelain")
	if err != nil {
		return nil, err
	}
	return parseStatus(out), nil
}

func parseStatus(out string) []FileStatus {
	if strings.TrimSpace(out) == "" {
		return nil
	}
	var files []FileStatus
	for _, line := range strings.Split(out, "\n") {
		if len(line) < 3 {
			continue
		}
		fs := FileStatus{
			StatusCode: line[0],
			IndexCode:  line[1],
			Path:       line[3:],
			Staged:     line[0] != ' ' && line[0] != '?',
		}
		files = append(files, fs)
	}
	return files
}

// StatusPorcelain runs `git status --porcelain -z` and parses into FileStatus.
// The -z flag uses null bytes instead of newlines, handling filenames with
// special characters and renames properly. Format: "XY PATH\0" or
// "XY ORIG -> PATH\0" for renames.
// Untracked directories are expanded into individual files.
func StatusPorcelain(dir string) ([]FileStatus, error) {
	out, err := runWithTimeout(dir, defaultTimeout, "status", "--porcelain", "-z")
	if err != nil {
		return nil, err
	}
	return parseStatusPorcelain(out, dir), nil
}

func parseStatusPorcelain(out string, dir string) []FileStatus {
	if len(out) == 0 {
		return nil
	}
	var files []FileStatus
	// -z output is null-byte separated entries
	entries := strings.Split(out, "\x00")
	for _, entry := range entries {
		entry = strings.TrimRight(entry, "\n")
		if len(entry) < 3 {
			continue
		}
		// First 2 chars are XY status codes
		statusCode := entry[0]
		indexCode := entry[1]
		path := entry[3:] // skip "XY "
		// Handle rename: "XY ORIG -> PATH"
		if idx := strings.Index(path, " -> "); idx >= 0 {
			path = path[idx+4:] // use the destination path
		}
		// Expand untracked directories into individual files
		if statusCode == '?' && strings.HasSuffix(path, "/") {
			files = expandUntrackedDir(files, dir, path[:len(path)-1])
			continue
		}
		fs := FileStatus{
			StatusCode: statusCode,
			IndexCode:  indexCode,
			Path:       path,
			Staged:     statusCode != ' ' && statusCode != '?',
		}
		files = append(files, fs)
	}
	return files
}

// expandUntrackedDir lists all files in an untracked directory recursively.
func expandUntrackedDir(files []FileStatus, baseDir, relDir string) []FileStatus {
	fullDir := filepath.Join(baseDir, relDir)
	entries, err := os.ReadDir(fullDir)
	if err != nil {
		// Can't read dir — add it as-is
		return append(files, FileStatus{
			StatusCode: '?',
			Path:       relDir + "/",
			Staged:     false,
		})
	}
	for _, entry := range entries {
		entryRel := filepath.Join(relDir, entry.Name())
		if entry.IsDir() {
			files = expandUntrackedDir(files, baseDir, entryRel)
		} else {
			files = append(files, FileStatus{
				StatusCode: '?',
				Path:       entryRel,
				Staged:     false,
			})
		}
	}
	return files
}

// ── Diff ──────────────────────────────────────────────────────────────────────

// Diff returns unified diff. If path is empty, returns all unstaged changes.
// If staged is true, returns staged changes (git diff --cached).
func Diff(dir string, path string, staged bool) (string, error) {
	args := []string{"diff"}
	if staged {
		args = append(args, "--cached")
	}
	if path != "" {
		args = append(args, "--", path)
	}
	return run(dir, args...)
}

// DiffFile returns diff for a single unstaged file.
func DiffFile(dir string, path string) (string, error) {
	return run(dir, "diff", "--", path)
}

// DiffNew returns the full content of an untracked file as a diff.
// For directories, lists all files inside with their content.
// Uses --no-index to compare against /dev/null, showing the file as all added lines.
func DiffNew(dir string, path string) (string, error) {
	fullPath := filepath.Join(dir, path)
	info, err := os.Stat(fullPath)
	if err != nil {
		return "", err
	}
	if info.IsDir() {
		return diffNewDir(dir, path)
	}
	// Read file content directly — more reliable than git diff --no-index
	content, err := os.ReadFile(fullPath)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	b.WriteString(fmt.Sprintf("new file %s\n", path))
	for _, line := range strings.Split(string(content), "\n") {
		b.WriteString("+" + line + "\n")
	}
	return b.String(), nil
}

// diffNewDir lists all files in an untracked directory with their content.
func diffNewDir(dir, dirPath string) (string, error) {
	fullDir := filepath.Join(dir, dirPath)
	entries, err := os.ReadDir(fullDir)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	for _, entry := range entries {
		relPath := filepath.Join(dirPath, entry.Name())
		if entry.IsDir() {
			continue // skip subdirs for now
		}
		// Show file header
		b.WriteString(fmt.Sprintf("new file %s\n", relPath))
		// Show content as added lines
		content, err := os.ReadFile(filepath.Join(fullDir, entry.Name()))
		if err != nil {
			b.WriteString(fmt.Sprintf("  (cannot read: %s)\n", err))
			continue
		}
		for _, line := range strings.Split(string(content), "\n") {
			b.WriteString("+" + line + "\n")
		}
		b.WriteString("\n")
	}
	return b.String(), nil
}

// DiffStaged returns `git diff --cached` for all staged changes.
func DiffStaged(dir string) (string, error) {
	return run(dir, "diff", "--cached")
}

// DiffCommit returns the full diff of a specific commit.
func DiffCommit(dir string, hash string) (string, error) {
	return run(dir, "diff", hash+"^.."+hash)
}

// ── Stage / Unstage ───────────────────────────────────────────────────────────

// Stage stages a file: `git add <path>`.
func Stage(dir string, path string) error {
	_, err := run(dir, "add", path)
	return err
}

// StageAll stages all changes: `git add -A`.
func StageAll(dir string) error {
	_, err := run(dir, "add", "-A")
	return err
}

// Unstage unstages a file: `git reset HEAD -- <path>`.
func Unstage(dir string, path string) error {
	_, err := run(dir, "reset", "HEAD", "--", path)
	return err
}

// UnstageAll unstages everything: `git reset HEAD`.
func UnstageAll(dir string) error {
	_, err := run(dir, "reset", "HEAD")
	return err
}

// ── Commit ────────────────────────────────────────────────────────────────────

// Commit runs `git commit` with the given subject and body.
// Body is optional — if empty, only subject is used.
func Commit(dir string, subject, body string) error {
	msg := subject
	if body != "" {
		msg = subject + "\n\n" + body
	}
	_, err := run(dir, "commit", "-m", msg)
	return err
}

// AmendCommit amends the last commit with the given subject and body.
// If subject is empty, only the staged changes are added (keeps original message).
func AmendCommit(dir string, subject, body string) error {
	args := []string{"commit", "--amend"}
	if subject != "" {
		msg := subject
		if body != "" {
			msg = subject + "\n\n" + body
		}
		args = append(args, "-m", msg)
	} else {
		args = append(args, "--no-edit")
	}
	_, err := run(dir, args...)
	return err
}

// DiscardChanges reverts unstaged changes for a file: `git checkout -- <path>`.
func DiscardChanges(dir string, path string) error {
	_, err := run(dir, "checkout", "--", path)
	return err
}

// DiscardAll unstages and discards all changes: `git checkout -- .`
func DiscardAll(dir string) error {
	_, err := run(dir, "checkout", "--", ".")
	return err
}

// DeleteUntracked removes untracked files: `git clean -fd`
func DeleteUntracked(dir string) error {
	_, err := run(dir, "clean", "-fd")
	return err
}

// ── Log ───────────────────────────────────────────────────────────────────────

// Log returns the last n commits.
func Log(dir string, n int) ([]CommitInfo, error) {
	if n <= 0 {
		n = 30
	}
	out, err := run(dir, "log", "--format=%h|%an|%ar|%s", fmt.Sprintf("-%d", n))
	if err != nil {
		return nil, err
	}
	return parseLog(out), nil
}

func parseLog(out string) []CommitInfo {
	if strings.TrimSpace(out) == "" {
		return nil
	}
	var commits []CommitInfo
	for _, line := range strings.Split(out, "\n") {
		parts := strings.SplitN(line, "|", 4)
		if len(parts) < 4 {
			continue
		}
		commits = append(commits, CommitInfo{
			Hash:    parts[0],
			Author:  parts[1],
			Date:    parts[2],
			Message: parts[3],
		})
	}
	return commits
}

// CommitFiles returns the list of files changed in a specific commit.
func CommitFiles(dir string, hash string) ([]string, error) {
	out, err := run(dir, "diff-tree", "--no-commit-id", "-r", "--name-only", hash)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(out) == "" {
		return nil, nil
	}
	var files []string
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			files = append(files, line)
		}
	}
	return files, nil
}

// DiffCommitFile returns the diff of a specific file within a commit.
func DiffCommitFile(dir string, hash string, path string) (string, error) {
	return run(dir, "diff", hash+"^.."+hash, "--", path)
}

// ── Branches ──────────────────────────────────────────────────────────────────

// Branches returns local branches with tracking info.
func Branches(dir string) ([]BranchInfo, error) {
	out, err := run(dir, "branch", "-vv")
	if err != nil {
		return nil, err
	}
	return parseBranches(out), nil
}

func parseBranches(out string) []BranchInfo {
	if strings.TrimSpace(out) == "" {
		return nil
	}
	var branches []BranchInfo
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		current := false
		if strings.HasPrefix(line, "* ") {
			current = true
			line = line[2:]
		} else if strings.HasPrefix(line, "  ") {
			line = line[2:]
		}
		// Format: "branchname [hash] message" or "branchname"
		nameEnd := strings.IndexByte(line, ' ')
		if nameEnd == -1 {
			branches = append(branches, BranchInfo{Name: line, Current: current})
			continue
		}
		name := line[:nameEnd]
		rest := strings.TrimSpace(line[nameEnd:])
		// Extract tracking from "[origin/branch]" pattern
		tracking := ""
		if idx := strings.Index(rest, "["); idx >= 0 {
			if end := strings.Index(rest[idx:], "]"); end >= 0 {
				tracking = rest[idx+1 : idx+end]
			}
		}
		branches = append(branches, BranchInfo{Name: name, Current: current, Tracking: tracking})
	}
	return branches
}

// CreateBranch runs `git checkout -b <name>`.
func CreateBranch(dir string, name string) error {
	_, err := run(dir, "checkout", "-b", name)
	return err
}

// DeleteBranch runs `git branch -d <name>`.
func DeleteBranch(dir string, name string) error {
	_, err := run(dir, "branch", "-d", name)
	return err
}

// BranchHead returns the HEAD commit hash of a branch.
func BranchHead(dir string, branch string) (string, error) {
	out, err := run(dir, "rev-parse", branch)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// CheckoutBranch runs `git checkout <name>`.
func CheckoutBranch(dir string, name string) error {
	_, err := run(dir, "checkout", name)
	return err
}

// MergeBranch merges source into the current branch.
func MergeBranch(dir string, source string) error {
	_, err := run(dir, "merge", source)
	return err
}

// BranchAheadBehind returns how many commits local is ahead and behind its remote tracking branch.
// Returns (0, 0) if no tracking branch or on error.
func BranchAheadBehind(dir string, branch string, tracking string) (ahead int, behind int) {
	if tracking == "" {
		return 0, 0
	}
	out, err := run(dir, "rev-list", "--left-right", "--count", tracking+"..."+branch)
	if err != nil {
		return 0, 0
	}
	parts := strings.Fields(strings.TrimSpace(out))
	if len(parts) != 2 {
		return 0, 0
	}
	// left = behind (commits in remote not in local), right = ahead (commits in local not in remote)
	fmt.Sscanf(parts[0], "%d", &behind)
	fmt.Sscanf(parts[1], "%d", &ahead)
	return ahead, behind
}

// BranchLastCommit returns the last commit message (first line) for a branch.
func BranchLastCommit(dir string, branch string) string {
	out, err := run(dir, "log", "-1", "--format=%s", branch)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}

// ── Stash ─────────────────────────────────────────────────────────────────────

// StashList returns all stash entries.
func StashList(dir string) ([]StashInfo, error) {
	out, err := run(dir, "stash", "list")
	if err != nil {
		return nil, err
	}
	return parseStash(out), nil
}

func parseStash(out string) []StashInfo {
	if strings.TrimSpace(out) == "" {
		return nil
	}
	var stashes []StashInfo
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		// Format: "stash@{0}: On branch: message"
		refEnd := strings.Index(line, ": ")
		if refEnd == -1 {
			stashes = append(stashes, StashInfo{Ref: line, Message: ""})
			continue
		}
		ref := line[:refEnd]
		msg := strings.TrimSpace(line[refEnd+2:])
		// Strip "On branch <name>: " prefix
		if idx := strings.Index(msg, ": "); idx >= 0 {
			msg = strings.TrimSpace(msg[idx+2:])
		}
		stashes = append(stashes, StashInfo{Ref: ref, Message: msg})
	}
	return stashes
}

// StashCreate runs `git stash push -u` to stash all changes including untracked files.
func StashCreate(dir string, message string) error {
	if message == "" {
		_, err := run(dir, "stash", "-u")
		return err
	}
	_, err := run(dir, "stash", "push", "-u", "-m", message)
	return err
}

// StashPop runs `git stash pop`. All changes come back as unstaged.
func StashPop(dir string) error {
	_, err := run(dir, "stash", "pop")
	return err
}

// StashApply runs `git stash apply`.
func StashApply(dir string) error {
	_, err := run(dir, "stash", "apply")
	return err
}

// StashDrop runs `git stash drop <ref>`.
func StashDrop(dir string, ref string) error {
	_, err := run(dir, "stash", "drop", ref)
	return err
}

// ── Remotes ───────────────────────────────────────────────────────────────────

// Remotes returns all remotes with their URLs.
func Remotes(dir string) ([]RemoteInfo, error) {
	out, err := run(dir, "remote", "-v")
	if err != nil {
		return nil, err
	}
	return parseRemotes(out), nil
}

func parseRemotes(out string) []RemoteInfo {
	if strings.TrimSpace(out) == "" {
		return nil
	}
	seen := map[string]bool{}
	var remotes []RemoteInfo
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		// Format: "name\turl\t(type)"
		parts := strings.Fields(line)
		if len(parts) < 2 {
			continue
		}
		name := parts[0]
		if seen[name] {
			continue
		}
		seen[name] = true
		url := parts[1]
		remotes = append(remotes, RemoteInfo{Name: name, URL: url})
	}
	return remotes
}

// RemoteAdd runs `git remote add <name> <url>`.
func RemoteAdd(dir string, name, url string) error {
	_, err := run(dir, "remote", "add", name, url)
	return err
}

// RemoteRemove runs `git remote remove <name>`.
func RemoteRemove(dir string, name string) error {
	_, err := run(dir, "remote", "remove", name)
	return err
}

// FetchAll runs `git fetch --all`.
func FetchAll(dir string) error {
	_, err := run(dir, "fetch", "--all")
	return err
}

// Push pushes the current branch to its upstream.
func Push(dir string) error {
	// Check if upstream is set
	if _, err := run(dir, "rev-parse", "--verify", "--quiet", "@{upstream}"); err != nil {
		// No upstream — push with --set-upstream so future pulls work
		_, err = run(dir, "push", "--set-upstream", "origin", "HEAD")
		return err
	}
	_, err := run(dir, "push")
	return err
}

// PushForce pushes with force lease (safe force push).
func PushForce(dir string) error {
	_, err := run(dir, "push", "--force-with-lease")
	return err
}

// Pull pulls from upstream with rebase.
func Pull(dir string) error {
	_, err := run(dir, "pull", "--rebase")
	return err
}

// RebaseInProgress returns true if a rebase is currently in progress.
func RebaseInProgress(dir string) bool {
	_, err := os.Stat(filepath.Join(dir, ".git", "REBASE_HEAD"))
	return err == nil
}

// MergeInProgress returns true if a merge is currently in progress.
func MergeInProgress(dir string) bool {
	_, err := os.Stat(filepath.Join(dir, ".git", "MERGE_HEAD"))
	return err == nil
}

// ConflictInProgress returns true if either a rebase or merge is in progress.
func ConflictInProgress(dir string) bool {
	return RebaseInProgress(dir) || MergeInProgress(dir)
}

// ConflictingFiles returns the list of files with unresolved merge conflicts.
func ConflictingFiles(dir string) ([]string, error) {
	out, err := run(dir, "diff", "--name-only", "--diff-filter=U")
	if err != nil {
		return nil, err
	}
	out = strings.TrimSpace(out)
	if out == "" {
		return nil, nil
	}
	return strings.Split(out, "\n"), nil
}

// ResolveOurs resolves a conflicting file by keeping the current branch version.
func ResolveOurs(dir string, file string) error {
	_, err := run(dir, "checkout", "--ours", file)
	if err != nil {
		return err
	}
	_, err = run(dir, "add", file)
	return err
}

// ResolveTheirs resolves a conflicting file by keeping the incoming branch version.
func ResolveTheirs(dir string, file string) error {
	_, err := run(dir, "checkout", "--theirs", file)
	if err != nil {
		return err
	}
	_, err = run(dir, "add", file)
	return err
}

// RebaseContinue continues an in-progress rebase after conflicts are resolved.
func RebaseContinue(dir string) error {
	_, err := run(dir, "rebase", "--continue")
	return err
}

// RebaseAbort aborts an in-progress rebase.
func RebaseAbort(dir string) error {
	_, err := run(dir, "rebase", "--abort")
	return err
}

// MergeContinue continues an in-progress merge after conflicts are resolved.
// (git merge --continue is an alias for git merge --no-commit, but we use commit)
func MergeContinue(dir string) error {
	_, err := run(dir, "commit", "--no-edit")
	return err
}

// MergeAbort aborts an in-progress merge.
func MergeAbort(dir string) error {
	_, err := run(dir, "merge", "--abort")
	return err
}

// HasUnpushedCommits returns true if the current branch has commits
// ahead of its upstream.
func HasUnpushedCommits(dir string) bool {
	out, err := run(dir, "log", "--oneline", "@{upstream}..HEAD")
	if err == nil {
		return strings.TrimSpace(out) != ""
	}
	// No upstream configured — try origin/<branch>
	branch, err2 := run(dir, "rev-parse", "--abbrev-ref", "HEAD")
	if err2 != nil {
		return false
	}
	branch = strings.TrimSpace(branch)
	out, err = run(dir, "log", "--oneline", "origin/"+branch+"..HEAD")
	if err != nil {
		// No remote branch either — branch was never pushed
		count, err3 := run(dir, "rev-list", "--count", "HEAD")
		return err3 == nil && strings.TrimSpace(count) != "0"
	}
	return strings.TrimSpace(out) != ""
}

// Prune runs `git remote prune origin`.
func Prune(dir string) error {
	_, err := run(dir, "remote", "prune", "origin")
	return err
}

// ── Tags ──────────────────────────────────────────────────────────────────────

// Tags returns all tags sorted by creation date (newest first).
func Tags(dir string) ([]TagInfo, error) {
	out, err := run(dir, "tag", "--sort=-creatordate")
	if err != nil {
		return nil, err
	}
	return parseTags(out), nil
}

func parseTags(out string) []TagInfo {
	if strings.TrimSpace(out) == "" {
		return nil
	}
	var tags []TagInfo
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		tags = append(tags, TagInfo{Name: line})
	}
	return tags
}

// TagCreate creates a tag. If msg is non-empty, creates an annotated tag (-a).
func TagCreate(dir string, name, msg string) error {
	if msg == "" {
		_, err := run(dir, "tag", name)
		return err
	}
	_, err := run(dir, "tag", "-a", name, "-m", msg)
	return err
}

// TagDelete runs `git tag -d <name>`.
func TagDelete(dir string, name string) error {
	_, err := run(dir, "tag", "-d", name)
	return err
}
