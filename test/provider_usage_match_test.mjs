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
  'source_errors',
  '第三方插件以后端完整权限运行',
  'plugin_store.registry_failed',
  '/logs',
  '/request-logs',
  '刷新列表',
  '自动刷新',
  '路径 / 方法',
  'IP / 归属地',
  '实际调用',
  '系统提示词',
  '提示词摘要',
  '输出摘要',
  'detailModal',
  '请求预览',
  '完整 MCP 功能介绍',
  'Skill 功能介绍和提示词',
  '全部可用工具',
  '排除模型',
  '优先级',
  'missing_account_id',
  'Chatgpt-Account-Id',
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
];

for (const token of mustNotContain) {
  assert.equal(source.includes(token), false, `${token} should not be present`);
}
