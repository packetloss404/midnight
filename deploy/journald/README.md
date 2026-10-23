# journald

S02 owns journald drop-ins and packaging.

`midnight.conf` provides conservative rate limiting and persistent journal
defaults for hosts that opt into package-managed journald configuration. It is
intended to be installed as `/etc/systemd/journald.conf.d/midnight.conf`.
