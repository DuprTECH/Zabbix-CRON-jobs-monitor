package cli

import (
	"reflect"
	"testing"
)

func TestParse(t *testing.T) {
	o, err := Parse([]string{"-n", "zaloha-db", "-t", "IT_ops", "/opt/zaloha.sh", "--db", "-v"})
	if err != nil {
		t.Fatal(err)
	}
	if o.Name != "zaloha-db" || o.Notify != "IT_ops" || !reflect.DeepEqual(o.Cmd, []string{"/opt/zaloha.sh", "--db", "-v"}) {
		t.Errorf("got %+v", o)
	}
	o, err = Parse([]string{"--", "/opt/x/backup.sh"})
	if err != nil || o.Name != "backup" {
		t.Errorf("got %+v %v", o, err)
	}
	if _, err := Parse([]string{"-n", "x"}); err != ErrNoCommand {
		t.Errorf("want ErrNoCommand, got %v", err)
	}
}

func TestDeriveName(t *testing.T) {
	cases := map[string][]string{
		"zaloha":     {"/opt/scripts/zaloha.sh", "--db"},
		"backup":     {"bash", "-e", "/opt/x/backup.sh"},
		"sync":       {"nice", "-n", "10", "timeout", "1h", "/opt/sync.py"},
		"cron.daily": {"run-parts", "--report", "/etc/cron.daily"},
		"sh":         {"sh", "-c", "echo hi"},
		"certbot":    {"sudo", "-u", "le", "/usr/bin/certbot", "renew"},
		"logrotate":  {"PATH=/x", "/usr/sbin/logrotate", "/etc/logrotate.conf"},
	}
	for want, cmd := range cases {
		if got := DeriveName(cmd); got != want {
			t.Errorf("DeriveName(%q) = %q, want %q", cmd, got, want)
		}
	}
	if Sanitize("my job,[1]") != "my_job_1" || Sanitize("/opt/scripts/zaloha.sh") != "/opt/scripts/zaloha.sh" {
		t.Error(Sanitize("my job,[1]"), Sanitize("/opt/scripts/zaloha.sh"))
	}
}
