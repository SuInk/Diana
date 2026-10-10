"""Run installer regression checks without downloads or touching a real installation."""
import os
from pathlib import Path
import subprocess
import tempfile
import unittest

SCRIPT = Path(__file__).with_name("install.sh").read_text()


def section(start, end):
    return SCRIPT[SCRIPT.index(start):SCRIPT.index(end, SCRIPT.index(start))]


class InstallerTests(unittest.TestCase):
    def run_shell(self, script, root, **env):
        return subprocess.run(
            ["sh", "-eu", "-c", script],
            env={**os.environ, "HOME": str(root), **env},
            text=True, capture_output=True,
        )

    def test_options_replace_environment_configuration(self):
        parser = section('repo=', '# 管道安装')
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            result = self.run_shell(
                'set -- --dir "$HOME/custom space" --host 0.0.0.0 --port 18081 --no-start --yes\n' +
                parser + '\nprintf "%s\n" "$install_dir" "$host" "$port" "$start_after_install" "$interactive"',
                root, DIANA_PORT='9999', DIANA_HOST='bad-host', DIANA_INSTALL_DIR='/wrong')
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertEqual(result.stdout.splitlines(),
                             [str(root / 'custom space'), '0.0.0.0', '18081', 'false', 'false'])
            result = self.run_shell(parser + '\nprintf "%s\n" "$host" "$port"',
                                    root, DIANA_PORT='9999', DIANA_HOST='bad-host')
            self.assertEqual(result.stdout.splitlines(), ['127.0.0.1', '18080'])

    def test_invalid_options_fail_before_installation(self):
        for args in [['--port'], ['--unknown']]:
            result = subprocess.run(['sh', str(Path(__file__).with_name('install.sh')), *args],
                                    capture_output=True, text=True)
            self.assertNotEqual(result.returncode, 0)
            self.assertIn('option' if args[0] == '--unknown' else 'Missing value', result.stderr)

    def test_scope(self):
        selection = section('# 默认安装给当前用户', '\nfail() {')
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            selection = selection.replace('/opt/diana', str(root / 'system'))
            for scope, uid, target, expected, succeeds in [
                ('auto', '501', '', 'user', True),
                ('user', '501', '', 'user', True),
                ('auto', '501', str(root / 'custom space'), 'user', True),
                ('system', '501', '', '', False),
                ('system', '0', '', 'system', True),
                ('auto', '0', '', 'system', True),
            ]:
                with self.subTest(scope=scope, uid=uid, target=target):
                    result = self.run_shell(
                        'id() { echo "$TEST_UID"; };\n' + selection +
                        '\nprintf "%s\\n%s" "$install_scope" "$install_dir"',
                        root, install_scope=scope, install_dir=target, TEST_UID=uid)
                    self.assertEqual(result.returncode == 0, succeeds, result.stderr)
                    if succeeds:
                        lines = result.stdout.splitlines()
                        self.assertEqual(lines[0], expected)
                        self.assertEqual(lines[1], target or str(
                            root / ('system' if expected == 'system' else '.local/share/diana')))
            (root / 'system').mkdir()
            (root / 'system/.installed-version').touch()
            result = self.run_shell(
                'id() { echo 501; };\n' + selection,
                root, install_scope='auto', install_dir='')
            self.assertNotEqual(result.returncode, 0)
            self.assertIn('Existing Diana installation', result.stderr)

    def test_existing_system_scope_is_preserved(self):
        selection = section('# 默认安装给当前用户', '\nfail() {')
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / '.install-scope').write_text('system\n')
            for uid in ['501', '0']:
                result = self.run_shell(
                    'id() { echo "$TEST_UID"; };\n' + selection +
                    '\nprintf "%s" "$install_scope"',
                    root, install_scope='auto', install_dir=str(root), TEST_UID=uid)
                self.assertEqual(result.returncode == 0, uid == '0')
                if uid == '0':
                    self.assertEqual(result.stdout, 'system')

    def test_persistent_path_is_idempotent(self):
        setup = section('ensure_command_dir_on_path() {', '\nif [ -f "$install_dir/uninstall.sh" ]')
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            for shell, rc in [('zsh', '.zshrc'), ('bash', '.bashrc'),
                              ('sh', '.profile'), ('fish', '.config/fish/conf.d/diana.fish')]:
                result = self.run_shell(
                    setup + '\ncommand_dir="$HOME/.local/bin"\n'
                    'ensure_command_dir_on_path\nensure_command_dir_on_path',
                    root, SHELL='/bin/' + shell, ZDOTDIR=str(root))
                self.assertEqual(result.returncode, 0, result.stderr)
                text = (root / rc).read_text()
                self.assertEqual(text.count('$HOME/.local/bin'), 1)

    def test_frontend_migration_preserves_custom_paths(self):
        helpers = section('yaml_quote() {', '\n# set_yaml_value')
        helpers += section('set_yaml_value() {', '\nassemble_macos_app')
        migration = section('# 旧安装升级为 .app', '\nif [ "$os" = "darwin" ]; then\n  # 每次启动')
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            old = str(root / 'frontend-next/dist')
            new = str(root / 'Diana.app/Contents/MacOS/frontend-next/dist')
            for source, expected in [
                ("'" + old + "'", new), ('"' + old + '"', new),
                ('./frontend-next/dist', new),
                ("'/custom/frontend'", '/custom/frontend'),
            ]:
                with self.subTest(source=source):
                    config = root / 'config.yaml'
                    config.write_text('server:\n  frontend_dist: ' + source +
                                      '\n  port: 18080\nadmin:\n  password: unchanged\n')
                    result = self.run_shell(
                        helpers + '\ninfo() { :; };\n' + migration +
                        '\nread_yaml_value "$config_file" server frontend_dist',
                        root, os='darwin', temp_dir=str(root), install_dir=str(root), config_file=str(config),
                        macos_app_dir=str(root / 'Diana.app'))
                    self.assertEqual(result.returncode, 0, result.stderr)
                    self.assertEqual(result.stdout.strip(), expected)
                    self.assertIn('password: unchanged', config.read_text())
                    self.assertIn('port: 18080', config.read_text())


if __name__ == '__main__':
    unittest.main()
