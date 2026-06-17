import { execFile as execFileCb } from 'child_process';
import { promisify } from 'util';
import fs from 'fs/promises';
import path from 'path';

const execFile = promisify(execFileCb);

export async function packProject(projectDir: string, outputPath: string): Promise<string> {
  await fs.mkdir(path.dirname(outputPath), { recursive: true });
  // tar -czf outputPath -C projectDir .
  await execFile('tar', ['-czf', outputPath, '-C', projectDir, '.']);
  return outputPath;
}
