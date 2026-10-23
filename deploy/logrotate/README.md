# logrotate

S02/S06 own log rotation defaults.

`midnight` rotates package-managed file logs under `/var/log/midnight`. Services
primarily log to journald in Phase 0; this file is present for components that
also write operational logs to disk.
