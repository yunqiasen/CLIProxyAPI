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
  '/v0/management/request-logs',
  'function aXRequestLogs(){',
  '{path:`/logs`,element:(0,R.jsx)(aXRequestLogs,{})}',
  'Dp.getState().managementKey',
  'usageTooltip',
  'data-cpa-usage-title',
  'data-cpa-usage-tooltip',
  'max-width:min(760px',
  'white-space:pre;',
  'overflow:auto',
  'width:max-content',
  'translateX(-100%)',
  'data-placement',
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
];

for (const token of mustNotContain) {
  assert.equal(source.includes(token), false, `${token} should not be present`);
}
