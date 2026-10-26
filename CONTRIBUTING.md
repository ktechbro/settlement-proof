# Contributing

Use `gofmt`, run `go vet ./...`, and run the tests with a real PostgreSQL database as described in the README. CI runs the race detector on Linux.

For changes involving recovery or monetary records, add a scenario showing the failure boundary and assert exact event identities and integer quantities. Avoid assertions based only on aggregate totals. Do not replace PostgreSQL integration tests with a mocked SQL interface.

Document changes to finality policy, scope identity, canonical event identity, coverage, or provider assumptions. Keep RPC credentials out of fixtures, errors, and committed configuration. Existing deployed schemas require explicit numbered migrations.

Keep observers read-only. A local fixture that generates transactions must be clearly separated from the service and must never require production signing keys.
