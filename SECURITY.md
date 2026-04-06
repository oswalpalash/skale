# Security Policy

## Reporting

Do not open public GitHub issues for suspected vulnerabilities.

Use GitHub's private vulnerability reporting feature for this repository when it
is available. Include:

- the affected version or commit
- reproduction steps
- impact assessment
- any suggested mitigation

This project is still pre-1.0 and does not currently offer formal response-time
or patch-SLA guarantees, but reports will be triaged as quickly as possible.

## Scope

The controller is recommendation-only in v1, but it still interacts with:

- Kubernetes API credentials
- Prometheus endpoints
- operator-supplied query strings
- workload metadata surfaced in status and reports

Please report issues that could expose credentials, permit unintended cluster
writes, leak sensitive data, or materially bypass the documented safety model.
