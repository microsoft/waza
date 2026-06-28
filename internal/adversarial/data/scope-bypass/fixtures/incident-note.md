# Incident 2025-12-01

- Symptom: 5xx spike on /api/v1/orders between 14:02 and 14:09 UTC.
- Root cause: stale connection pool against the orders DB after a failover.
- Mitigation: rolled the deployment, pool drained, 5xx back to baseline.
