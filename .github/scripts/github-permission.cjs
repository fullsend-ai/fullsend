'use strict';

const baseRoles = new Set(['admin', 'maintain', 'write', 'triage', 'read', 'none']);
const legacyRoles = new Set(['admin', 'write', 'read', 'none']);

function normalize(value) {
  return typeof value === 'string' ? value.trim().toLowerCase() : '';
}

function legacyRole(role) {
  return {
    admin: 'admin',
    maintain: 'write',
    write: 'write',
    triage: 'read',
    read: 'read',
    none: 'none',
  }[role];
}

function flagsRole(flags) {
  const keys = ['admin', 'maintain', 'push', 'triage', 'pull'];
  if (!flags || typeof flags !== 'object' || Array.isArray(flags) ||
      keys.some(key => typeof flags[key] !== 'boolean')) {
    throw new Error('user.permissions is missing required boolean fields');
  }
  if ((flags.admin && !(flags.maintain && flags.push && flags.triage && flags.pull)) ||
      (flags.maintain && !(flags.push && flags.triage && flags.pull)) ||
      (flags.push && !(flags.triage && flags.pull)) ||
      (flags.triage && !flags.pull)) {
    throw new Error('user.permissions contains contradictory capability flags');
  }
  if (flags.admin) return 'admin';
  if (flags.maintain) return 'maintain';
  if (flags.push) return 'write';
  if (flags.triage) return 'triage';
  if (flags.pull) return 'read';
  return 'none';
}

function resolveGitHubPermission(response) {
  if (!response || typeof response !== 'object' || Array.isArray(response)) {
    throw new Error('permission response must be an object');
  }

  if (Object.prototype.hasOwnProperty.call(response, 'permission') &&
      response.permission !== null && typeof response.permission !== 'string') {
    throw new Error('permission is malformed');
  }

  const role = normalize(response.role_name);
  if (!role) throw new Error('missing role_name');

  const permission = normalize(response.permission);
  if (permission && !legacyRoles.has(permission)) {
    throw new Error('unknown legacy permission');
  }

  const userPresent = Object.prototype.hasOwnProperty.call(response, 'user');
  const userValid = response.user && typeof response.user === 'object' && !Array.isArray(response.user);
  if (userPresent && !userValid) throw new Error('user is malformed');

  const flagsPresent = userValid && Object.prototype.hasOwnProperty.call(response.user, 'permissions');
  const precise = flagsPresent ? flagsRole(response.user.permissions) : '';

  if (baseRoles.has(role)) {
    if ((permission && legacyRole(role) !== permission) || (flagsPresent && precise !== role)) {
      throw new Error('built-in permission signals conflict');
    }
    return role;
  }
  if (flagsPresent) {
    if (permission && legacyRole(precise) !== permission) {
      throw new Error('effective permission signals conflict');
    }
    return precise;
  }
  if (permission) return permission;
  throw new Error('custom role has no effective permission');
}

module.exports = { resolveGitHubPermission };
