# Code Mode runtime

Model JavaScript runs in process: `internal/codemode` drives the QuickJS-NG
WASM build from `quickjs-wasi` with wazero and evaluates the prelude from
`@earendil-works/pi-codemode`, which defines `tools`, `store`/`load`,
`console`, `text`/`image`/`exit` and the bridge to Go. Tool requests reach the
Go callback, which must enforce tool permissions, schemas, budgets, hooks and
auditing before executing a request. Production needs no Node or npm; each
execution gets a fresh WASM instance with a 32 MiB QuickJS heap, a 64 MiB
linear-memory cap, a stack guard and an interrupt on the deadline.

## Rebuild and validate

Node is only needed here, to copy and verify the embedded files:

```sh
npm ci --ignore-scripts --prefix scripts/codemode
npm run build --prefix scripts/codemode
npm run check --prefix scripts/codemode
npm audit --omit=dev --prefix scripts/codemode
DIANA_TEST_RACE=1 scripts/test-serial.sh ./internal/codemode
```

`build` copies `quickjs.wasm`, writes the prelude source to `prelude.js`, and
copies both MIT licenses into `internal/codemode`. Dependencies are exact
versions with npm integrity hashes; install with `npm ci`, not an unlocked
install. Run `build` whenever the dependencies change; `check` fails when an
embedded file differs from the locked package. The Go side calls the
`qjs_*` exports of this exact `quickjs-wasi` build, so a version bump must also
be checked against `internal/codemode/vm.go`.
