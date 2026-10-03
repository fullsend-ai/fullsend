'use strict';

const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const test = require('node:test');
const { resolveGitHubPermission } = require('./github-permission.cjs');

const sharedCases = JSON.parse(fs.readFileSync(
  path.resolve(__dirname, '../../internal/forge/testdata/github_permission_cases.json'),
  'utf8'
));

const maintainFlags = {
  admin: false,
  maintain: true,
  push: true,
  triage: true,
  pull: true,
};

for (const fixture of sharedCases) {
  test(`shared fixture: ${fixture.name}`, () => {
    if (fixture.want_error) {
      assert.throws(() => resolveGitHubPermission(fixture.payload));
    } else {
      assert.equal(resolveGitHubPermission(fixture.payload), fixture.want);
    }
  });
}

test('keeps built-in roles', () => {
  assert.equal(resolveGitHubPermission({ role_name: 'write' }), 'write');
});

test('resolves a custom role from compatible effective signals', () => {
  assert.equal(resolveGitHubPermission({
    permission: 'write',
    role_name: 'ODH Repo Maintainer',
    user: { permissions: maintainFlags },
  }), 'maintain');
});

test('resolves a custom role from precise signals alone', () => {
  assert.equal(resolveGitHubPermission({
    role_name: 'ODH Repo Maintainer',
    user: { permissions: maintainFlags },
  }), 'maintain');
});

test('uses the conservative legacy fallback', () => {
  assert.equal(resolveGitHubPermission({ permission: 'read', role_name: 'Custom Triage' }), 'read');
});

for (const [name, payload] of [
  ['missing signals', { role_name: 'Custom' }],
  ['null flags', { permission: 'write', role_name: 'Custom', user: { permissions: null } }],
  ['malformed user', { permission: 'write', role_name: 'Custom', user: null }],
  ['conflicting signals', {
    permission: 'read',
    role_name: 'Custom',
    user: { permissions: { ...maintainFlags, maintain: false } },
  }],
  ['contradictory hierarchy', {
    permission: 'write',
    role_name: 'Custom',
    user: { permissions: { ...maintainFlags, push: false } },
  }],
]) {
  test(`rejects ${name}`, () => {
    assert.throws(() => resolveGitHubPermission(payload));
  });
}

for (const permission of [42, false, true, {}, []]) {
  test(`rejects non-string permission ${JSON.stringify(permission)}`, () => {
    assert.throws(() => resolveGitHubPermission({ role_name: 'write', permission }));
  });
}
