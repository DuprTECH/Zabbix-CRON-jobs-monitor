// Package crontab finds cron jobs in system and user crontabs.
package crontab

import (
	"bufio"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/DuprTECH/Zabbix-CRON-jobs-monitor/internal/cli"
	"github.com/DuprTECH/Zabbix-CRON-jobs-monitor/internal/cronexpr"
)

// WrapperName is the command that marks a cron job as monitored.
const WrapperName = "zbxntfy"

// Job is one cron entry.
type Job struct {
	Name      string `json:"job"`
	User      string `json:"user"`
	Schedule  string `json:"schedule"`
	Command   string `json:"cmd"`
	Source    string `json:"source"`
	Notify    string `json:"notify"`
	Monitored int    `json:"monitored"` // 1 = wrapped with zbxntfy
	TZ        string `json:"tz,omitempty"`

	sched *cronexpr.Schedule
	loc   *time.Location

	// for zbxntfy-wrap
	file     string // real path of the crontab
	lineNo   int    // 1-based line number
	rawLine  string // line as it is in the file
	insertAt int    // byte offset in rawLine where "zbxntfy " can be inserted, -1 if unknown
	baseName string // name before de-duplication
}

// Times returns the previous and next activation (unix seconds, 0 if none).
func (j *Job) Times(now time.Time) (prev, next int64) {
	if j.sched == nil {
		return 0, 0
	}
	t := now.In(j.loc)
	if p, ok := j.sched.Prev(t); ok {
		prev = p.Unix()
	}
	if n, ok := j.sched.Next(t); ok {
		next = n.Unix()
	}
	return
}

// Scanner reads crontabs below Root ("" or "/" for the real system).
type Scanner struct {
	Root string
}

func (s Scanner) p(elem ...string) string {
	return filepath.Join(append([]string{s.Root, "/"}, elem...)...)
}

// Scan returns all cron jobs found and a list of non-fatal problems.
func (s Scanner) Scan() ([]Job, []string) {
	var (
		jobs []Job
		errs []string
	)
	add := func(file string, system bool, user string) {
		j, e := parseFile(file, system, user)
		jobs = append(jobs, j...)
		errs = append(errs, e...)
	}

	if _, err := os.Stat(s.p("etc/crontab")); err == nil {
		add(s.p("etc/crontab"), true, "")
	}
	for _, f := range listFiles(s.p("etc/cron.d"), &errs, true) {
		add(f, true, "")
	}
	// Debian/Ubuntu: /var/spool/cron/crontabs/<user>; RHEL/SUSE: /var/spool/cron/<user>
	for _, dir := range []string{s.p("var/spool/cron/crontabs"), s.p("var/spool/cron/tabs"), s.p("var/spool/cron")} {
		for _, f := range listFiles(dir, &errs, false) {
			add(f, false, filepath.Base(f))
		}
	}

	// Display paths relative to the scanned root.
	for i := range jobs {
		if rel, err := filepath.Rel(s.p(), jobs[i].Source); err == nil {
			jobs[i].Source = "/" + filepath.ToSlash(rel)
		}
	}
	errs = append(errs, dedupe(jobs)...)
	return jobs, errs
}

// FilePath returns the real path of the crontab the job comes from.
func (j *Job) FilePath() string { return j.file }

// LineNo returns the 1-based line number of the job in its crontab.
func (j *Job) LineNo() int { return j.lineNo }

// RawLine returns the job's line exactly as it is in the crontab.
func (j *Job) RawLine() string { return j.rawLine }

// SystemCrontab reports whether the job is in /etc/crontab or /etc/cron.d
// (with a user field), as opposed to a user crontab in /var/spool/cron.
func (j *Job) SystemCrontab() bool {
	return !strings.Contains(filepath.ToSlash(j.Source), "/var/spool/cron/")
}

var ignoredName = regexp.MustCompile(`(^\.|~$|\.(dpkg-[a-z]+|rpm[a-z]+|ucf-[a-z]+|swp|bak|orig|disabled)$|^placeholder$)`)

func listFiles(dir string, errs *[]string, runParts bool) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if !os.IsNotExist(err) {
			*errs = append(*errs, fmt.Sprintf("%s: %v", dir, err))
		}
		return nil
	}
	var out []string
	for _, e := range entries {
		if e.Type()&fs.ModeType != 0 && e.Type()&fs.ModeSymlink == 0 {
			continue // directories, devices, ...
		}
		if ignoredName.MatchString(e.Name()) {
			continue
		}
		if runParts && strings.Contains(e.Name(), ".") {
			continue // Debian cron ignores /etc/cron.d files with dots
		}
		full := filepath.Join(dir, e.Name())
		if st, err := os.Stat(full); err != nil || !st.Mode().IsRegular() {
			continue
		}
		out = append(out, full)
	}
	return out
}

var (
	envLine   = regexp.MustCompile(`^([A-Za-z_][A-Za-z0-9_]*)\s*=\s*(.*)$`)
	userField = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.@-]*\$?$`)
)

func parseFile(file string, system bool, user string) ([]Job, []string) {
	f, err := os.Open(file)
	if err != nil {
		return nil, []string{fmt.Sprintf("%s: %v", file, err)}
	}
	defer f.Close()

	var (
		jobs []Job
		errs []string
		tz   string
		loc  = time.Local
	)
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for n := 1; sc.Scan(); n++ {
		raw := sc.Text()
		line := strings.TrimSpace(raw)
		lead := len(raw) - len(strings.TrimLeft(raw, " \t\r\n\v\f"))
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if m := envLine.FindStringSubmatch(line); m != nil {
			if m[1] == "CRON_TZ" {
				tz = strings.Trim(m[2], `"'`)
				if l, err := time.LoadLocation(tz); err == nil {
					loc = l
				} else {
					errs = append(errs, fmt.Sprintf("%s:%d: unknown CRON_TZ %q", file, n, tz))
					tz, loc = "", time.Local
				}
			}
			continue
		}
		j, err := parseLine(line, system, user)
		if err != nil {
			errs = append(errs, fmt.Sprintf("%s:%d: %v", file, n, err))
			continue
		}
		j.Source = file
		j.TZ = tz
		j.loc = loc
		j.file = file
		j.lineNo = n
		j.rawLine = raw
		if j.insertAt >= 0 {
			j.insertAt += lead
		}
		j.baseName = j.Name
		jobs = append(jobs, *j)
	}
	if err := sc.Err(); err != nil {
		errs = append(errs, fmt.Sprintf("%s: %v", file, err))
	}
	return jobs, errs
}

// parseLine parses one crontab entry.
func parseLine(line string, system bool, user string) (*Job, error) {
	fields := strings.Fields(line)
	nsched := 5
	if strings.HasPrefix(fields[0], "@") {
		nsched = 1
	}
	need := nsched + 1
	if system {
		need++
	}
	if len(fields) < need {
		return nil, fmt.Errorf("incomplete entry %q", line)
	}
	spec := strings.Join(fields[:nsched], " ")
	sched, err := cronexpr.Parse(spec)
	if err != nil {
		return nil, err
	}
	rest := afterFields(line, nsched)
	if system {
		user = strings.Fields(rest)[0]
		if !userField.MatchString(user) {
			return nil, fmt.Errorf("invalid user field %q", user)
		}
		rest = afterFields(rest, 1)
	}
	restOff := len(line) - len(rest)
	cmd := cronCommand(rest)

	j := &Job{User: user, Schedule: spec, Command: MaskSecrets(cmd), Notify: "default", sched: sched}
	var at int
	// identify on the raw text (up to the first unescaped %), so that word
	// offsets point into the line; "\%" tokenizes to "%" just like cron does.
	j.Name, j.Monitored, j.Notify, at = identify(rawCronCommand(rest))
	j.insertAt = -1
	if at >= 0 {
		j.insertAt = restOff + at
	}
	return j, nil
}

// rawCronCommand returns s up to the first unescaped '%', unchanged.
func rawCronCommand(s string) string {
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+1 < len(s) {
			i++
			continue
		}
		if s[i] == '%' {
			return s[:i]
		}
	}
	return s
}

// afterFields returns s without its first n whitespace-separated fields.
func afterFields(s string, n int) string {
	s = strings.TrimLeft(s, " \t")
	for i := 0; i < n; i++ {
		k := strings.IndexAny(s, " \t")
		if k < 0 {
			return ""
		}
		s = strings.TrimLeft(s[k:], " \t")
	}
	return s
}

// cronCommand applies cron's '%' rule: an unescaped % ends the command (the
// rest is stdin); "\%" is a literal percent sign.
func cronCommand(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+1 < len(s) && s[i+1] == '%' {
			b.WriteByte('%')
			i++
			continue
		}
		if s[i] == '%' {
			break
		}
		b.WriteByte(s[i])
	}
	return strings.TrimSpace(b.String())
}

// commands that never are "the job" itself
var helperCommands = map[string]bool{
	"cd": true, "test": true, "[": true, "[[": true, "true": true, "false": true,
	":": true, "{": true, "}": true, "if": true, "then": true, "else": true,
	"fi": true, "sleep": true, "export": true, "exit": true, "umask": true,
	"set": true, "source": true, ".": true, "echo": true,
}

// keywords that may precede the actual command in a simple command
var shellKeywords = map[string]bool{
	"if": true, "then": true, "else": true, "elif": true, "do": true,
	"while": true, "until": true, "{": true, "!": true, "exec": true,
}

// identify returns the job name, whether it is wrapped with zbxntfy, its
// notification tag value and the byte offset in cmd where "zbxntfy " would be
// inserted (the start of the job's command, -1 if none).
func identify(cmd string) (name string, monitored int, notify string, insertAt int) {
	cmds := simpleCommands(cmd)
	for _, ws := range cmds {
		words := texts(ws)
		for i, w := range words {
			if path.Base(w) == WrapperName {
				o, err := cli.Parse(words[i+1:])
				if err != nil {
					break // broken invocation: treat as not monitored
				}
				notify = o.Notify
				if notify == "" {
					notify = "default"
				}
				return o.Name, 1, notify, -1
			}
		}
	}
	for _, ws := range cmds {
		words := texts(ws)
		k := 0
		for k < len(words) && (envLine.MatchString(words[k]) || shellKeywords[words[k]]) {
			k++
		}
		if k < len(words) && !helperCommands[path.Base(words[k])] {
			return cli.Sanitize(cli.DeriveName(words[k:])), 0, "default", ws[k].pos
		}
	}
	if len(cmds) > 0 {
		return cli.Sanitize(cli.DeriveName(texts(cmds[0]))), 0, "default", cmds[0][0].pos
	}
	return "job", 0, "default", -1
}

// dedupe keeps job names unique: monitored entries win, then the first one
// seen; remaining duplicates get a numeric suffix so nothing is hidden.
// Two monitored jobs with the same name are reported, because the wrapper of
// the second one would report into the first one's items.
func dedupe(jobs []Job) (errs []string) {
	idx := make([]int, len(jobs))
	for i := range idx {
		idx[i] = i
	}
	sort.SliceStable(idx, func(a, b int) bool { return jobs[idx[a]].Monitored > jobs[idx[b]].Monitored })
	seen := map[string]int{}
	for _, i := range idx {
		base := jobs[i].Name
		if seen[base] == 0 {
			seen[base] = 1
			continue
		}
		if jobs[i].Monitored == 1 {
			errs = append(errs, fmt.Sprintf("%s: duplicate job name %q, add -n <name> to zbxntfy", jobs[i].Source, base))
		}
		for {
			seen[base]++
			cand := fmt.Sprintf("%s-%d", base, seen[base])
			if seen[cand] == 0 {
				jobs[i].Name = cand
				seen[cand] = 1
				break
			}
		}
	}
	return errs
}
