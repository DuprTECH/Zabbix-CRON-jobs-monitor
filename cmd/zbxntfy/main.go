// zbxntfy runs a command (typically from cron), passes its output through
// unchanged and reports the run to Zabbix via zabbix_sender:
//
//	job.start[<job>]     unix time the run started   (sent before the command runs)
//	job.log[<job>]       stdout+stderr (tail, max -m bytes)
//	job.end[<job>]       unix time the run finished
//	job.duration[<job>]  seconds
//	job.rc[<job>]        exit code (128+N when killed by signal N)
//
// The exit code of zbxntfy is the exit code of the command.
package main

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/DuprTECH/Zabbix-CRON-jobs-monitor/internal/cli"
)

var version = "dev"

const (
	configFile = "/etc/zbxntfy.conf"
	usage      = `Usage: zbxntfy [options] [--] command [args...]

Runs the command and reports exit code, duration and output to Zabbix.
Example crontab entries:

  0 2 * * *  zbxntfy /opt/scripts/backup.sh
  0 3 * * *  zbxntfy -n backup-db -g dba /opt/scripts/backup.sh --db

Options:
`
)

var agentConfigs = []string{"/etc/zabbix/zabbix_agent2.conf", "/etc/zabbix/zabbix_agentd.conf"}

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	o, err := cli.Parse(args)
	if err != nil {
		fmt.Fprint(os.Stderr, usage)
		fs := cli.NewFlagSet(&cli.Options{})
		fs.SetOutput(os.Stderr)
		fs.PrintDefaults()
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		if errors.Is(err, cli.ErrNoCommand) {
			return 2
		}
		fmt.Fprintln(os.Stderr, "\nzbxntfy:", err)
		return 2
	}
	if o.Version {
		fmt.Println("zbxntfy", version)
		return 0
	}
	applyConfigFile(o)

	s := &sender{o: o}
	key := func(k string) string { return fmt.Sprintf("job.%s[%s]", k, o.Name) }

	start := time.Now()
	s.send(item{key("start"), strconv.FormatInt(start.Unix(), 10)})

	buf := newTailBuffer(o.MaxBytes)
	rc := execute(o.Cmd, buf)
	end := time.Now()

	log := buf.String()
	if log == "" {
		log = "(no output)"
	}
	s.send(item{key("log"), log})
	s.send(
		item{key("end"), strconv.FormatInt(end.Unix(), 10)},
		item{key("duration"), strconv.FormatFloat(end.Sub(start).Seconds(), 'f', 3, 64)},
		item{key("rc"), strconv.Itoa(rc)},
	)
	return rc
}

// execute runs the command, copying its output to our stdout/stderr and to buf.
func execute(argv []string, buf *tailBuffer) int {
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = io.MultiWriter(os.Stdout, buf)
	cmd.Stderr = io.MultiWriter(os.Stderr, buf)

	if err := cmd.Start(); err != nil {
		msg := fmt.Sprintf("zbxntfy: cannot run %s: %v\n", argv[0], err)
		fmt.Fprint(os.Stderr, msg)
		buf.Write([]byte(msg))
		if errors.Is(err, exec.ErrNotFound) || errors.Is(err, fs.ErrNotExist) {
			return 127
		}
		return 126
	}

	sigs := make(chan os.Signal, 4)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP, syscall.SIGQUIT)
	done := make(chan struct{})
	go func() {
		for {
			select {
			case sig := <-sigs:
				_ = cmd.Process.Signal(sig)
			case <-done:
				return
			}
		}
	}()
	err := cmd.Wait()
	close(done)
	signal.Stop(sigs)

	if err == nil {
		return 0
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		if ws, ok := ee.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
			msg := fmt.Sprintf("zbxntfy: %s killed by signal %d (%v)\n", argv[0], int(ws.Signal()), ws.Signal())
			buf.Write([]byte(msg))
			return 128 + int(ws.Signal())
		}
		return ee.ExitCode()
	}
	buf.Write([]byte("zbxntfy: " + err.Error() + "\n"))
	return 1
}

// tailBuffer keeps the last max bytes written to it.
type tailBuffer struct {
	mu    sync.Mutex
	max   int
	buf   []byte
	total int64
}

func newTailBuffer(max int) *tailBuffer {
	if max <= 0 {
		max = cli.DefaultMaxBytes
	}
	return &tailBuffer{max: max}
}

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.total += int64(len(p))
	t.buf = append(t.buf, p...)
	if len(t.buf) > 2*t.max {
		t.buf = append(t.buf[:0], t.buf[len(t.buf)-t.max:]...)
	}
	return len(p), nil
}

func (t *tailBuffer) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	b := t.buf
	if len(b) <= t.max {
		return strings.ToValidUTF8(string(b), "?")
	}
	b = b[len(b)-t.max:]
	// start at a line boundary (and never in the middle of a UTF-8 rune)
	if i := bytes.IndexByte(b, '\n'); i >= 0 && i < len(b)-1 {
		b = b[i+1:]
	}
	for len(b) > 0 && !utf8.RuneStart(b[0]) {
		b = b[1:]
	}
	hdr := fmt.Sprintf("[zbxntfy: output truncated, last %d of %d bytes]\n", len(b), t.total)
	return hdr + strings.ToValidUTF8(string(b), "?")
}

// applyConfigFile fills unset options from /etc/zbxntfy.conf (optional):
//
//	agent_config = /etc/zabbix/zabbix_agent2.conf
//	hostname     = my-host
//	server       = zabbix-proxy.example.com
//	port         = 10051
//	sender       = /usr/bin/zabbix_sender
func applyConfigFile(o *cli.Options) {
	path := configFile
	if p := os.Getenv("ZBXNTFY_CONFIG"); p != "" {
		path = p
	}
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		k, v = strings.ToLower(strings.TrimSpace(k)), strings.Trim(strings.TrimSpace(v), `"'`)
		switch k {
		case "agent_config":
			setIfEmpty(&o.AgentConfig, v)
		case "hostname":
			setIfEmpty(&o.Hostname, v)
		case "server":
			setIfEmpty(&o.Server, v)
		case "sender":
			setIfEmpty(&o.Sender, v)
		case "port":
			if o.Port == 0 {
				o.Port, _ = strconv.Atoi(v)
			}
		}
	}
}

func setIfEmpty(dst *string, v string) {
	if *dst == "" {
		*dst = v
	}
}

type item struct{ key, value string }

type sender struct {
	o        *cli.Options
	baseArgs []string
	resolved bool
	disabled bool
}

func (s *sender) resolve() {
	s.resolved = true
	o := s.o
	if o.Sender == "" {
		o.Sender = "zabbix_sender"
	}
	if o.AgentConfig == "" && o.Server == "" {
		for _, c := range agentConfigs {
			if _, err := os.Stat(c); err == nil {
				o.AgentConfig = c
				break
			}
		}
	}
	if o.AgentConfig == "" && o.Server == "" {
		s.warn("no Zabbix agent config found (%s); use -c or -z/-s", strings.Join(agentConfigs, ", "))
		s.disabled = true
		return
	}
	if o.AgentConfig != "" {
		s.baseArgs = append(s.baseArgs, "-c", o.AgentConfig)
	}
	if o.Server != "" {
		s.baseArgs = append(s.baseArgs, "-z", o.Server)
	}
	if o.Port != 0 {
		s.baseArgs = append(s.baseArgs, "-p", strconv.Itoa(o.Port))
	}
	if o.Hostname != "" {
		s.baseArgs = append(s.baseArgs, "-s", o.Hostname)
	}
}

func (s *sender) warn(format string, a ...any) {
	if !s.o.Quiet {
		fmt.Fprintf(os.Stderr, "zbxntfy: "+format+"\n", a...)
	}
}

// send sends one value with -k/-o (supports multi-line values) or several
// single-line values in one batch via -i -.
func (s *sender) send(items ...item) {
	if s.o.DryRun {
		for _, it := range items {
			fmt.Fprintf(os.Stderr, "zbxntfy [dry-run] %s = %q\n", it.key, it.value)
		}
		return
	}
	if !s.resolved {
		s.resolve()
	}
	if s.disabled {
		return
	}
	args := append([]string{}, s.baseArgs...)
	var stdin io.Reader
	if len(items) == 1 {
		args = append(args, "-k", items[0].key, "-o", items[0].value)
	} else {
		var in strings.Builder
		for _, it := range items {
			fmt.Fprintf(&in, "- %s %s\n", it.key, it.value)
		}
		args = append(args, "-i", "-")
		stdin = strings.NewReader(in.String())
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, s.o.Sender, args...)
	cmd.Stdin = stdin
	out, err := cmd.CombinedOutput()
	if err != nil {
		hint := ""
		if bytes.Contains(out, []byte("failed: ")) && !bytes.Contains(out, []byte("failed: 0")) {
			hint = " (are the items discovered on this host? check template 'Cron jobs by zbxntfy' and the job name)"
		}
		s.warn("zabbix_sender %s failed: %v%s\n%s", items[0].key, err, hint, strings.TrimSpace(string(out)))
		if errors.Is(err, exec.ErrNotFound) {
			s.disabled = true
		}
	}
}
