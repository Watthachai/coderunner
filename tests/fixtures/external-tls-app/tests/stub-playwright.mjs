import fs from 'node:fs';
// The checker installs Chromium through @playwright/test's CLI; the fixture has no browser tests.
fs.mkdirSync('node_modules/@playwright/test', { recursive: true });
fs.writeFileSync('node_modules/@playwright/test/cli.js', 'process.exit(0);\n');
