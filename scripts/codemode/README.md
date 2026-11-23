# Code Mode runtime

Model JavaScript runs in process: `internal/codemode` runs it in a fresh
[moejs](https://github.com/MoeForge/moejs) runtime per execution and evaluates
the prelude from `@earendil-works/pi-codemode`, which defines `tools`,
`store`/`load`, `console`, `text`/`image`/`exit` and the bridge to Go. Tool
requests reach the Go callback, which must enforce tool permissions, schemas,
budgets, hooks and auditing before executing a request. Production needs no
Node or npm; each runtime has its own copy of the builtins (the prelude freezes
them), a 32 MiB memory limit and an interrupt on the deadline.

## Rebuild and validate

Node is only needed here, to copy and verify the embedded prelude:

```sh
npm ci --ignore-scripts --prefix scripts/codemode
npm run build --prefix scripts/codemode
npm run check --prefix scripts/codemode
npm audit --omit=dev --prefix scripts/codemode
DIANA_TEST_RACE=1 scripts/test-serial.sh ./internal/codemode
```

`build` writes the prelude source to `prelude.js` and copies the MIT license
into `internal/codemode`. Dependencies are exact versions with npm integrity
hashes; install with `npm ci`, not an unlocked install. Run `build` whenever
the dependency changes; `check` fails when an embedded file differs from the
locked package.
