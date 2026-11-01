# Code Mode runtime

The trusted Node host executes model JavaScript in the isolated QuickJS/WASM VM
provided by `@earendil-works/pi-codemode`. Tool requests cross JSONL IPC to Go;
the host has no local tool implementations. The Go caller must enforce tool
permissions, schemas, budgets, hooks, and auditing before executing a request.

## Rebuild and validate

```sh
npm ci --ignore-scripts --prefix scripts/codemode
npm test --prefix scripts/codemode
npm run build --prefix scripts/codemode
npm run check --prefix scripts/codemode
npm audit --omit=dev --prefix scripts/codemode
DIANA_TEST_RACE=1 scripts/test-serial.sh ./internal/codemode
```

The committed `internal/codemode/runtime.json.gz` embeds the host, dependency
JavaScript, QuickJS WASM, package manifests, and licenses in the Go binary.
It is a sorted JSON map of paths to base64 bytes, compressed with gzip. No
bundler is required. Dependencies are exact versions with npm integrity hashes;
install with `npm ci`, not an unlocked install. Run `build` whenever the host or
dependencies change. `check` compares every decompressed byte with the canonical
payload, detecting stale embedded files without false failures from different
Node/zlib compression versions.

`codemode.Prepare` requires existing Node >=22.19, extracts the archive into a
private temporary directory, and returns a cleanup function. Runtime operation
does not install npm dependencies or access the network to obtain them. Go tests
exercise the embedded archive, not the source worktree's `node_modules`; they
skip only when Node is absent. The caller must expose Code Mode only after
runtime preparation succeeds and retain ordinary tool execution without Node.
The Runner bridge supports permission-checked child calls and compact script
results. Registry creation opts in with `Config.CodeModeEnabled`; the main reply
configuration enables it and includes `codemode` in resident tools. An absent or
unsupported Node leaves ordinary tools available without exposing Code Mode.
This source configuration does not imply an existing production process has
been upgraded. See the replay report under `docs/testing` for live validation.

## Boundaries

- Script: 64 KiB; VM heap: 32 MiB; timeout: at most 120 seconds.
- Child calls: at most 32; each input: 64 KiB; each IPC frame: 1 MiB.
- Final value and text output: 32 KiB; oversized results explicitly fail.
- `store(key, value)` / `load(key)` retain at most 512 KiB within one Runner
  request, including successive scripts. Save raw tool data before selecting
  fields so a later script can inspect more without refetching. Successful
  scripts commit atomically; failed or over-limit scripts do not change the
  store. `store(key, undefined)` deletes a key. Store state travels separately
  over IPC and is removed from model results and audit outputs. It is reset on
  both entry to and exit from Runner.Run and is not part of conversation carryover.
- Successful `browser_render` results with complete page-read evidence are also
  reused automatically for identical normalized arguments within the same
  request, for at most two minutes, 32 entries and 512 KiB. Failed/partial reads
  are not cached. `codemode({code, fresh:true})` bypasses and invalidates the old
  matching entry. Hits still pass current permissions, hooks and tool budgets;
  audit steps and observer metadata mark `cache_hit`, and script results report
  `cache_hits`. Original retrieval timestamps remain unchanged. This is a tool
  result cache, distinct from the model provider's prompt cache.
- Model scripts receive no Node process, filesystem, module loader, or direct
  network APIs. The registered Go tools are the capability boundary.
- Node inherits only path/temp-directory/platform variables, not credentials or
  Node preload options. Parent cancellation kills the process group and cancels
  child callbacks. Callbacks must honor their context; Execute waits for them.
- This is VM isolation, not an OS/container sandbox for the trusted Node host.

## Sources and licensing

- `@earendil-works/pi-codemode@1.4.0`: MIT, from `earendil-works/pi`.
- Its npm package omits the license file; `licenses/pi-codemode-MIT.txt` is from
  the repository LICENSE at commit
  `b556cfe5b0300a91f39c7bc5369a65a25fb7fcd5` (retrieved 2026-11-01).
- `quickjs-wasi@3.6.2`: MIT; license copied from the integrity-locked npm package.

All license notices are included in the embedded archive and extracted runtime.
