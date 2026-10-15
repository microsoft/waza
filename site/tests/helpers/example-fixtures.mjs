import assert from 'node:assert/strict';
import { randomUUID } from 'node:crypto';
import { lstatSync, mkdirSync, realpathSync, rmSync, statSync } from 'node:fs';
import { dirname, isAbsolute, join, resolve } from 'node:path';
import { homedir } from 'node:os';

export function validateFixtureAncestry(directory) {
  let ancestor = realpathSync(directory);
  for (;;) {
    for (const marker of ['skills', '.waza.yaml', '.git']) {
      const path = join(ancestor, marker);
      try {
        lstatSync(path);
      } catch (error) {
        if (error.code === 'ENOENT') continue;
        throw error;
      }
      throw new Error(`Docs fixture inherits project marker ${path}; select external fixture storage`);
    }
    const parent = dirname(ancestor);
    if (parent === ancestor) return;
    ancestor = parent;
  }
}

export function offlineEnvironment(directory, inherited = process.env) {
  const physical = realpathSync(directory);
  assert.ok(isAbsolute(physical) && statSync(physical).isDirectory());
  const temporary = join(physical, 'workspaces');
  const guard = join(physical, 'copilot-runtime-denied');
  mkdirSync(temporary, { mode: 0o700 });
  mkdirSync(guard, { mode: 0o700 });
  return {
    ...inherited,
    CI: '1',
    NO_COLOR: '1',
    WAZA_NO_UPDATE_CHECK: '1',
    ENABLE_COPILOT_TESTS: 'false',
    ENABLE_COPILOT_SDK_TESTS: 'false',
    ENABLE_LIVE_COPILOT_TESTS: 'false',
    COPILOT_CLI_PATH: realpathSync(guard),
    TMPDIR: temporary,
    TMP: temporary,
    TEMP: temporary,
    GIT_CEILING_DIRECTORIES: physical,
  };
}

export function allocateExampleFixture(repository, inherited = process.env) {
  const cache = process.platform === 'darwin' ? join(homedir(), 'Library', 'Caches')
    : process.platform === 'win32' ? inherited.LOCALAPPDATA ?? join(homedir(), 'AppData', 'Local')
      : inherited.XDG_CACHE_HOME ?? join(homedir(), '.cache');
  const base = resolve(inherited.WAZA_DOCS_FIXTURE_ROOT ?? join(cache, 'waza-docs-examples'));
  assert.notEqual(realpathSync(repository), base, 'Fixture storage must be outside the checkout');
  mkdirSync(base, { recursive: true, mode: 0o700 });
  validateFixtureAncestry(base);
  const directory = join(realpathSync(base), `example-${process.pid}-${randomUUID()}`);
  mkdirSync(directory, { mode: 0o700 });
  try {
    validateFixtureAncestry(directory);
    return {
      directory,
      env: offlineEnvironment(directory, inherited),
      cleanup: () => rmSync(directory, { recursive: true, force: true }),
    };
  } catch (error) {
    rmSync(directory, { recursive: true, force: true });
    throw error;
  }
}

export function replaceExactlyOnce(text, original, replacement) {
  const index = text.indexOf(original);
  assert.ok(index >= 0 && text.indexOf(original, index + original.length) === -1,
    `Expected exactly one occurrence of ${JSON.stringify(original)}`);
  return text.slice(0, index) + replacement + text.slice(index + original.length);
}
