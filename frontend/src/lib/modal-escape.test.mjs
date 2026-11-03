import assert from 'node:assert/strict';
import test from 'node:test';
import { readFileSync } from 'node:fs';
import vm from 'node:vm';
import ts from 'typescript';
import { parse } from '@vue/compiler-sfc';

const script = parse(readFileSync(new URL('../components/Modal.vue', import.meta.url), 'utf8')).descriptor.scriptSetup.content;
const ast = ts.createSourceFile('modal.ts', script, ts.ScriptTarget.Latest, true, ts.ScriptKind.TS);
const fn = ast.statements.find(node => ts.isFunctionDeclaration(node) && node.name?.text === 'onKeydown');
const code = ts.transpileModule(fn.getText(ast), { compilerOptions: { target: ts.ScriptTarget.ES2022 } }).outputText;

test('Escape closes only the topmost dialog and preserves the parent settings form', () => {
  const parent = {}, child = {}, closed = [];
  const dialogs = [parent, child];
  const context = vm.createContext({ document: { querySelectorAll: () => dialogs }, modalRef: { value: parent }, emit: event => closed.push(event) });
  vm.runInContext(code, context);
  context.onKeydown({ key: 'Escape' });
  assert.deepEqual(closed, []);
  context.modalRef.value = child;
  context.onKeydown({ key: 'Escape' });
  assert.deepEqual(closed, ['close']);
  dialogs.pop();
  context.modalRef.value = parent;
  context.onKeydown({ key: 'Enter' });
  assert.equal(closed.length, 1);
  context.onKeydown({ key: 'Escape' });
  assert.equal(closed.length, 2);
});
