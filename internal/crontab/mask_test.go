package crontab

import "testing"

func TestMaskSecrets(t *testing.T) {
	cases := map[string]string{
		"API_TOKEN=s3cr3tvalue python3 /root/x.py --apply":       "API_TOKEN=*** python3 /root/x.py --apply",
		`DB_PASSWORD="a b c" /opt/backup.sh`:                     "DB_PASSWORD=*** /opt/backup.sh",
		"api_token='xyz' run":                                    "api_token=*** run",
		"/opt/x --password=hunter2 --verbose":                    "/opt/x --password=*** --verbose",
		"/opt/x --api-key abc123 -v":                             "/opt/x --api-key *** -v",
		"mysqldump -uroot -pS3cret db > /tmp/db.sql":             "mysqldump -uroot -p*** db > /tmp/db.sql",
		"curl -s https://user:pa55@example.com/hook":             "curl -s https://user:***@example.com/hook",
		"PATH=/usr/sbin:/usr/bin /usr/sbin/rotate-logs":          "PATH=/usr/sbin:/usr/bin /usr/sbin/rotate-logs",
		"cd / && run-parts --report /etc/cron.hourly":            "cd / && run-parts --report /etc/cron.hourly",
		"test -e /run/systemd/system || SERVICE_MODE=1 /sbin/e2": "test -e /run/systemd/system || SERVICE_MODE=1 /sbin/e2",
	}
	for in, want := range cases {
		if got := MaskSecrets(in); got != want {
			t.Errorf("MaskSecrets(%q)\n got %q\nwant %q", in, got, want)
		}
	}
}
