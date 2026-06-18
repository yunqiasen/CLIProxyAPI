import assert from 'node:assert/strict';
import fs from 'node:fs';

const source = fs.readFileSync(new URL('../static/management.html', import.meta.url), 'utf8');

const mustContain = [
  'api-key-usage',
  'success_details',
  'failure_details',
  'modelChips',
  'modelSearchTerms',
  'existingApiKey',
  'delete_plugin',
  'delete_restart_required',
  'quota-refresh-jobs',
  'download-zip',
  '/request-logs',
  '刷新列表',
  '自动刷新',
  '完整 MCP 功能介绍',
  'Skill 功能介绍和提示词',
  '全部可用工具',
  'usageTooltip',
  'usageTooltipPortal',
  'closeOnOverlayClick',
  'max-width:min(760px,100vw - 40px)',
  'overflow:auto',
  'width:max-content',
  'translate(-100%)',
];

for (const token of mustContain) {
  assert.equal(source.includes(token), true, `${token} not found`);
}

const mustNotContain = [
  'function cpaProviderUsageProviderForElement',
  'function cpaInstallProviderUsageDetailsPatch',
  'cpaProviderUsageHover',
  '{path:`/logs`,element:(0,R.jsx)(L$,{})}',
  'Zf.getState().managementKey',
  'cpaDownloadBlob',
  'cpa-fork-regression-tokens',
  'data-cpa-usage-tooltip',
  'function aXRequestLogs(){',
];

for (const token of mustNotContain) {
  assert.equal(source.includes(token), false, `${token} should not be present`);
}
