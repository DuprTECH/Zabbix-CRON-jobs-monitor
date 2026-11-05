package crontab

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestIdentify(t *testing.T) {
	cases := []struct {
		cmd, name, notify string
		monitored         int
	}{
		{"/opt/scripts/zaloha.sh", "zaloha", "default", 0},
		{"/opt/scripts/zaloha.sh > /dev/null 2>&1", "zaloha", "default", 0},
		{"zbxntfy /opt/scripts/zaloha.sh", "zaloha", "default", 1},
		{"/usr/local/bin/zbxntfy -n zaloha-db -t IT_ops /opt/scripts/zaloha.sh --db >/dev/null 2>&1", "zaloha-db", "IT_ops", 1},
		{"cd /opt/app && zbxntfy ./run.sh", "run", "default", 1},
		{"zbxntfy -n /opt/scripts/zaloha.sh /opt/scripts/zaloha.sh", "/opt/scripts/zaloha.sh", "default", 1},
		{"cd /opt/app && ./run.sh", "run", "default", 0},
		{"cd / && run-parts --report /etc/cron.hourly", "cron.hourly", "default", 0},
		{"test -x /usr/sbin/anacron || { cd / && run-parts --report /etc/cron.daily; }", "cron.daily", "default", 0},
		{"nice -n 10 ionice -c3 /usr/bin/python3 /opt/sync/sync.py --full", "sync", "default", 0},
		{"flock -n /tmp/x.lock /opt/bin/report.sh", "report", "default", 0},
		{"FOO=bar zbxntfy bash /opt/x/backup.sh", "backup", "default", 1},
		{`zbxntfy -n "my job" sh -c 'a; b'`, "my_job", "default", 1},
		{"[ -x /usr/lib/php/sessionclean ] && if [ ! -d /run/systemd/system ]; then /usr/lib/php/sessionclean; fi", "sessionclean", "default", 0},
	}
	for _, c := range cases {
		n, m, g, _ := identify(c.cmd)
		if n != c.name || m != c.monitored || g != c.notify {
			t.Errorf("identify(%q) = %q,%d,%q; want %q,%d,%q", c.cmd, n, m, g, c.name, c.monitored, c.notify)
		}
	}
}

func TestSimpleCommands(t *testing.T) {
	var got [][]string
	for _, ws := range simpleCommands(`a "b c" 'd' 2>&1 >> /var/log/x.log; e|f && g 2> err &> all`) {
		got = append(got, texts(ws))
	}
	want := [][]string{{"a", "b c", "d"}, {"e"}, {"f"}, {"g"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %q want %q", got, want)
	}
}

func write(t *testing.T, root, rel, content string) {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestScan(t *testing.T) {
	root := t.TempDir()
	write(t, root, "etc/crontab", `SHELL=/bin/sh
PATH=/usr/local/sbin:/usr/local/bin:/sbin:/bin:/usr/sbin:/usr/bin
17 *	* * *	root    cd / && run-parts --report /etc/cron.hourly
25 6	* * *	root	test -x /usr/sbin/anacron || { cd / && run-parts --report /etc/cron.daily; }
`)
	write(t, root, "etc/cron.d/backup", `# backups
CRON_TZ=UTC
0 2 * * 1-5 root zbxntfy -t IT_ops /opt/scripts/zaloha.sh
@reboot root /opt/scripts/warmup.sh
bad line here
`)
	write(t, root, "etc/cron.d/skip.dpkg-old", "* * * * * root /bin/true\n")
	write(t, root, "var/spool/cron/crontabs/alice", `MAILTO=""
*/5 * * * * /home/alice/bin/poll.py
0 3 * * * zbxntfy /opt/scripts/zaloha.sh
0 4 * * * date +\%F%stdin text
`)

	jobs, errs := Scanner{Root: root}.Scan()
	var got []string
	for _, j := range jobs {
		got = append(got, strings.Join([]string{j.Name, j.User, j.Schedule, j.Notify, j.Source, map[int]string{0: "-", 1: "M"}[j.Monitored], j.TZ}, "|"))
	}
	want := []string{
		"cron.hourly|root|17 * * * *|default|/etc/crontab|-|",
		"cron.daily|root|25 6 * * *|default|/etc/crontab|-|",
		"zaloha|root|0 2 * * 1-5|IT_ops|/etc/cron.d/backup|M|UTC",
		"warmup|root|@reboot|default|/etc/cron.d/backup|-|UTC",
		"poll|alice|*/5 * * * *|default|/var/spool/cron/crontabs/alice|-|",
		"zaloha-2|alice|0 3 * * *|default|/var/spool/cron/crontabs/alice|M|",
		"date|alice|0 4 * * *|default|/var/spool/cron/crontabs/alice|-|",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("jobs:\n got %q\nwant %q", got, want)
	}
	if len(errs) != 2 || !strings.Contains(errs[0], "backup:5") || !strings.Contains(errs[1], "duplicate job name") {
		t.Errorf("errs = %q", errs)
	}
	if jobs[6].Command != "date +%F" {
		t.Errorf("percent handling: %q", jobs[6].Command)
	}

	now := time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC) // Saturday
	prev, next := jobs[2].Times(now)
	if time.Unix(prev, 0).UTC().Day() != 6 || time.Unix(next, 0).UTC().Day() != 9 {
		t.Errorf("weekday job times: %v %v", time.Unix(prev, 0).UTC(), time.Unix(next, 0).UTC())
	}
	if p, n := jobs[3].Times(now); p != 0 || n != 0 {
		t.Errorf("@reboot times = %d %d", p, n)
	}
}
