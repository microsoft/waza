const mode = process.argv[2];
if (mode === 'echo-failure') {
  for await (const chunk of process.stdin) process.stdout.write(chunk);
  process.stderr.write('intentional transport test failure\n');
  process.exitCode = 7;
} else if (mode === 'hang') {
  process.on('SIGTERM', () => {});
  console.log('ready');
  setInterval(() => {}, 1_000);
} else if (mode === 'flood') {
  setInterval(() => process.stdout.write('x'.repeat(1024)), 1);
} else if (mode === 'docker') {
  const { appendFile, readFile } = await import('node:fs/promises');
  const args = process.argv.slice(3);
  await appendFile(process.env.REFERENCE_TEST_LOG, JSON.stringify(args) + '\n');
  if (args[0] === 'create') {
    if (process.env.REFERENCE_TEST_CREATE_STATUS) {
      console.error('intentional transport test create failure');
      process.exitCode = Number(process.env.REFERENCE_TEST_CREATE_STATUS);
    } else {
      console.log('c'.repeat(64));
    }
  } else if (args[0] === 'start') {
    let input = '';
    for await (const chunk of process.stdin) input += chunk;
    if (process.env.REFERENCE_TEST_REPLY) {
      process.stdout.write(await readFile(process.env.REFERENCE_TEST_REPLY));
    } else {
      process.stdout.write(input);
    }
    process.exitCode = Number(process.env.REFERENCE_TEST_STATUS ?? 0);
  } else if (args[0] === 'rm') {
    process.exitCode = Number(process.env.REFERENCE_TEST_CLEANUP_STATUS ?? 0);
  } else {
    throw new Error(`Unexpected transport fixture command: ${args[0]}`);
  }
} else {
  throw new Error(`Unknown test mode: ${mode}`);
}
