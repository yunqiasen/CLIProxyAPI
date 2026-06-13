import assert from 'node:assert/strict';
import fs from 'node:fs';
import vm from 'node:vm';

function extractFunction(source, name) {
  const start = source.indexOf(`function ${name}(`);
  if (start < 0) throw new Error(`${name} not found`);
  const bodyStart = source.indexOf('{', start);
  let depth = 0;
  for (let i = bodyStart; i < source.length; i += 1) {
    const ch = source[i];
    if (ch === '{') depth += 1;
    if (ch === '}') {
      depth -= 1;
      if (depth === 0) return source.slice(start, i + 1);
    }
  }
  throw new Error(`${name} body not closed`);
}

class FakeElement {
  constructor(text, parentElement = null, tagName = 'DIV') {
    this._text = text;
    this.parentElement = parentElement;
    this.tagName = tagName;
  }

  get textContent() {
    return this._text;
  }
}

const source = fs.readFileSync(new URL('../static/management.html', import.meta.url), 'utf8');
const fnSource = extractFunction(source, 'cpaProviderUsageProviderForElement');
const context = {globalThis: {}};
vm.runInNewContext(`${fnSource}; globalThis.pick = cpaProviderUsageProviderForElement;`, context);
const pick = context.globalThis.pick;

const providers = [
  {provider: 'codex', providerKey: 'codex', success: 50, failed: 1, successDetails: [{model: 'gpt-5.5', status: 200, count: 50}], failureDetails: [{model: 'gpt-5.5', status: 500, error: 'context canceled', count: 1}], entries: [
    {provider: 'codex', providerKey: 'codex', success: 50, failed: 1, successDetails: [{model: 'gpt-5.5', status: 200, count: 50}], failureDetails: [{model: 'gpt-5.5', status: 500, error: 'context canceled', count: 1}]},
    {provider: 'codex', providerKey: 'codex', success: 0, failed: 0, successDetails: [], failureDetails: []},
  ]},
  {provider: '英伟达', providerKey: '英伟达', success: 7, failed: 19, successDetails: [{model: 'minimax', status: 200, count: 7}], failureDetails: [{model: 'minimax', status: 500, error: 'empty_stream', count: 19}], entries: [
    {provider: '英伟达', providerKey: '英伟达', success: 7, failed: 19, successDetails: [{model: 'minimax', status: 200, count: 7}], failureDetails: [{model: 'minimax', status: 500, error: 'empty_stream', count: 19}]},
    {provider: '英伟达', providerKey: '英伟达', success: 0, failed: 0, successDetails: [], failureDetails: []},
  ]},
  {provider: '薄荷', providerKey: '薄荷', success: 33, failed: 0, successDetails: [{model: 'gemini', status: 200, count: 33}], failureDetails: [], entries: [
    {provider: '薄荷', providerKey: '薄荷', success: 33, failed: 0, successDetails: [{model: 'gemini', status: 200, count: 33}], failureDetails: []},
  ]},
];

const page = new FakeElement('codex 成功: 50 失败: 1 英伟达 成功: 7 失败: 19 薄荷 成功: 33 失败: 0', null, 'MAIN');
const nvidiaCard = new FakeElement('英伟达 成功: 7 失败: 19', page, 'SECTION');
const nvidiaFailed = new FakeElement('失败: 19', nvidiaCard, 'SPAN');
assert.equal(pick(nvidiaFailed, providers)?.provider, '英伟达');

const looseFailed = new FakeElement('失败: 19', page, 'SPAN');
assert.equal(pick(looseFailed, providers)?.provider, '英伟达');

const ambiguousZero = new FakeElement('失败: 0', page, 'SPAN');
assert.equal(pick(ambiguousZero, providers), null);

const body = new FakeElement(page.textContent, null, 'BODY');
const detachedStats = new FakeElement('成功: 7 失败: 19', body, 'SPAN');
assert.equal(pick(detachedStats, providers)?.provider, '英伟达');

const codexCard = new FakeElement('codex 成功: 50 失败: 1 成功: 0 失败: 0', page, 'SECTION');
const codexZeroRow = new FakeElement('成功: 0 失败: 0', codexCard, 'DIV');
assert.equal(pick(codexZeroRow, providers), null);

const codexZeroSuccess = new FakeElement('成功: 0', codexZeroRow, 'SPAN');
assert.equal(pick(codexZeroSuccess, providers), null);

const codexNonzeroRow = new FakeElement('成功: 50 失败: 1', codexCard, 'DIV');
assert.equal(pick(codexNonzeroRow, providers)?.provider, 'codex');
