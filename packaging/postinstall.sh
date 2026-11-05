#!/bin/sh
# Allow the agent (user zabbix) to read user crontabs in /var/spool/cron.
if command -v setcap >/dev/null 2>&1; then
    setcap cap_dac_read_search+ep /usr/bin/zbxntfy-discovery || \
        echo "zbxntfy: setcap failed; user crontabs will not be discovered" >&2
else
    echo "zbxntfy: setcap not found (install libcap2-bin / libcap); user crontabs will not be discovered" >&2
fi

# Register the UserParameter with every installed agent.
for d in /etc/zabbix/zabbix_agent2.d /etc/zabbix/zabbix_agentd.d; do
    if [ -d "$d" ]; then
        ln -sf /usr/share/zbxntfy/userparameter_zbxntfy.conf "$d/userparameter_zbxntfy.conf"
    fi
done

for s in zabbix-agent2 zabbix-agent; do
    if command -v systemctl >/dev/null 2>&1 && systemctl is-active --quiet "$s"; then
        systemctl restart "$s" || true
    fi
done
exit 0
