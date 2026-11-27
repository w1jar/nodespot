# Security policy

## Reporting a vulnerability

Please report security problems **privately**, using GitHub private vulnerability reporting:

1. Open the **Security** tab of this repository (<https://github.com/w1jar/nodespot/security>).
2. Choose **Report a vulnerability** and describe the problem.

Please include the nodespot version, what you did, what happened and, if you can, how to reproduce it. Do not include real passwords or other private data.

**Please do not open public issues, discussions or pull requests for security problems.** We will acknowledge your report, keep you informed and credit you in the release notes if you wish.

## In scope

- the `nodespot` service (web interface and API)
- `nodespot-helper`, the privileged helper
- the `.deb` packages and the apt repository
- the install script (`install.sh`)

## Out of scope

- problems in AllStarLink, Asterisk or the operating system themselves (report those upstream)
- issues that need an already logged-in administrator to attack their own node
- exposing the web port (8080 by default) to the internet without TLS (not a supported setup)

## Supported versions

Security fixes are made in the latest release. Please update first (Settings, Updates, or `sudo apt update && sudo apt upgrade`).
