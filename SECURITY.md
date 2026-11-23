# Security

## Reporting a vulnerability

Report it privately, through GitHub's private vulnerability reporting: the
repository's Security tab, then "Report a vulnerability", or
[this form](https://github.com/scttymn/gantry/security/advisories/new).
Please don't open a public issue or pull request for it.

Say what's affected (a package, `gantry` or `bin/gantry`, or what `gantry new`
writes), the version, and how to reproduce it. The report stays private
between you and the maintainer until a fix is released.

## Supported versions

Fixes go into the latest release only. An app picks one up by bumping gantry
in its `go.mod`, which upgrades `bin/gantry` and Houston with it.
