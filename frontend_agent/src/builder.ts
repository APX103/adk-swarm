import { execFile as execFileCb } from 'child_process';
import { promisify } from 'util';
import fs from 'fs/promises';
import path from 'path';
import { spawn } from 'child_process';

const execFile = promisify(execFileCb);

export async function writeProjectFiles(projectDir: string, files: Record<string, string>): Promise<void> {
  await fs.mkdir(projectDir, { recursive: true });
  for (const [relativePath, content] of Object.entries(files)) {
    const fullPath = path.join(projectDir, relativePath);
    await fs.mkdir(path.dirname(fullPath), { recursive: true });
    await fs.writeFile(fullPath, content, 'utf-8');
  }
}

export async function fixMissingImports(projectDir: string): Promise<void> {
  const srcDir = path.join(projectDir, 'src');
  try {
    const relativeFiles = await fs.readdir(srcDir, { recursive: true });
    for (const relativePath of relativeFiles) {
      if (!relativePath.endsWith('.jsx')) continue;
      const filePath = path.join(srcDir, relativePath);
      const stat = await fs.stat(filePath);
      if (!stat.isFile()) continue;
      const content = await fs.readFile(filePath, 'utf-8');
      const cssImports = [...content.matchAll(/import\s+['"](\.\/[^'"]+\.css)['"]/g)].map((m) => m[1]);
      for (const relativeCss of cssImports) {
        const cssPath = path.join(path.dirname(filePath), relativeCss);
        try {
          await fs.access(cssPath);
        } catch {
          console.log(`[builder] Creating missing CSS file ${relativeCss} imported by ${relativePath}`);
          await fs.mkdir(path.dirname(cssPath), { recursive: true });
          await fs.writeFile(cssPath, '/* auto-generated placeholder */\n', 'utf-8');
        }
      }
    }
  } catch (err) {
    console.log('[builder] fixMissingImports skipped:', (err as Error).message);
  }
}

async function runNpm(args: string[], cwd: string, timeoutMs = 300000): Promise<void> {
  console.log(`[builder] Running npm ${args.join(' ')} in ${cwd}`);
  await execFile('npm', args, { cwd, timeout: timeoutMs, env: process.env });
}

export async function installDependencies(projectDir: string): Promise<void> {
  await runNpm(['install'], projectDir);
}

export async function buildProject(projectDir: string): Promise<void> {
  await runNpm(['run', 'build'], projectDir);
}

export async function verifyDevServer(projectDir: string, port = 5173, timeoutMs = 60000): Promise<void> {
  console.log(`[builder] Starting dev server on port ${port}`);
  const proc = spawn('npm', ['run', 'dev', '--', '--port', String(port), '--host', '127.0.0.1'], {
    cwd: projectDir,
    stdio: 'ignore',
    shell: false,
    detached: true,
    env: { ...process.env, BROWSER: 'none' },
  });

  const cleanup = () => {
    if (proc.pid) {
      try {
        process.kill(-proc.pid, 'SIGTERM');
      } catch {
        proc.kill('SIGTERM');
      }
    }
  };

  try {
    const startTime = Date.now();
    let ok = false;
    while (Date.now() - startTime < timeoutMs) {
      await sleep(1000);
      try {
        const res = await fetch(`http://127.0.0.1:${port}`);
        if (res.status === 200) {
          ok = true;
          console.log(`[builder] Dev server responded with 200`);
          break;
        }
      } catch {
        // not ready yet
      }
    }
    if (!ok) {
      throw new Error('Dev server did not respond with 200 in time.');
    }
  } finally {
    cleanup();
  }
}

function sleep(ms: number): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, ms));
}
