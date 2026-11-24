// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

// 编译桌面端自带的本机 Diana：内嵌 WebUI 的 diana-webui，放到 resources/bin/<os>-<arch>/，
// 打包时 electron-builder 按目标平台挑对应目录放进安装包。
//
//   npm run server                    # 当前系统和架构
//   npm run server -- mac-x64 win-x64 # 指定目标，<os> 用 electron-builder 的 mac / win / linux

import { execFileSync } from 'node:child_process'
import { existsSync, mkdirSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'

const desktop = join(dirname(fileURLToPath(import.meta.url)), '..')
const root = join(desktop, '..')
const goos = { mac: 'darwin', win: 'windows', linux: 'linux' }
const goarch = { x64: 'amd64', arm64: 'arm64' }
const hostOS = { darwin: 'mac', win32: 'win' }[process.platform] ?? 'linux'

const targets = process.argv.slice(2)
if (!targets.length) targets.push(`${hostOS}-${process.arch}`)

const run = (command, args, cwd = root, env = process.env) => execFileSync(command, args, { cwd, env, stdio: 'inherit', shell: process.platform === 'win32' })

// 前端没编进 cmd/webui/webdist 时，编出来的程序打不开管理后台，先补上。
if (!existsSync(join(root, 'cmd/webui/webdist/index.html'))) {
  if (!existsSync(join(root, 'frontend/dist/index.html'))) {
    if (!existsSync(join(root, 'frontend/node_modules'))) run('npm', ['ci'], join(root, 'frontend'))
    run('npm', ['run', 'build'], join(root, 'frontend'))
  }
  run('sh', ['scripts/embed-frontend.sh'])
}

for (const target of targets) {
  const [os, arch] = target.split('-')
  if (!goos[os] || !goarch[arch]) throw new Error(`不认识的目标 ${target}，形如 mac-arm64、win-x64、linux-x64`)
  const out = join(desktop, 'resources', 'bin', target)
  mkdirSync(out, { recursive: true })
  const binary = join(out, os === 'win' ? 'diana-webui.exe' : 'diana-webui')
  console.log(`编译 ${target} → ${binary}`)
  run('go', ['build', '-trimpath', '-o', binary, './cmd/webui'], root, { ...process.env, GOOS: goos[os], GOARCH: goarch[arch], CGO_ENABLED: '0' })
}
