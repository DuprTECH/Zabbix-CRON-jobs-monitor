#!/bin/sh
# On removal (not on upgrade) drop the UserParameter links.
case "$1" in
    upgrade|1) exit 0 ;;  # deb: "upgrade", rpm: $1 = remaining installs
esac
for d in /etc/zabbix/zabbix_agent2.d /etc/zabbix/zabbix_agentd.d; do
    rm -f "$d/userparameter_zbxntfy.conf"
done
exit 0
