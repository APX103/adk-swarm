import {
  BaseAgent,
  type BaseAgentConfig,
  type InvocationContext,
  createEvent,
  createEventActions,
} from '@google/adk';
import type { Content, Part } from '@google/genai';
import fs from 'fs/promises';
import path from 'path';

import { generateProjectFiles } from './generator.js';
import {
  buildProject,
  fixMissingImports,
  installDependencies,
  verifyDevServer,
  writeProjectFiles,
} from './builder.js';
import { packProject } from './packer.js';

function extractText(content?: Content): string {
  if (!content?.parts) return '';
  // The first text part is the actual user request; ADK appends context/tool
  // metadata in subsequent parts that would confuse the code generator.
  const first = content.parts.find((p: Part) => p.text);
  return first?.text?.trim() || '';
}

export class FrontendAgent extends BaseAgent {
  constructor(config: BaseAgentConfig) {
    super(config);
  }

  async *runAsyncImpl(
    context: InvocationContext,
  ): AsyncGenerator<ReturnType<typeof createEvent>, void, void> {
    const requestText = extractText(context.userContent);
    const taskId = context.invocationId;
    const projectDir = `/workspace/projects/${taskId}`;
    const downloadDir = `/workspace/downloads/${taskId}`;
    const archivePath = path.join(downloadDir, 'project.tar.gz');

    try {
      yield this.statusEvent(context, 'Generating Vite + React project files...');
      const files = await generateProjectFiles(requestText);

      yield this.statusEvent(context, 'Writing project files...');
      await writeProjectFiles(projectDir, files);
      await fixMissingImports(projectDir);

      yield this.statusEvent(context, 'Installing dependencies...');
      await installDependencies(projectDir);

      yield this.statusEvent(context, 'Building project...');
      await buildProject(projectDir);

      yield this.statusEvent(context, 'Verifying dev server...');
      await verifyDevServer(projectDir);

      yield this.statusEvent(context, 'Packing project archive...');
      await packProject(projectDir, archivePath);

      // Save the archive as an A2A artifact as well.
      if (context.artifactService) {
        const buf = await fs.readFile(archivePath);
        await context.artifactService.saveArtifact({
          appName: context.appName,
          userId: context.userId,
          sessionId: context.session.id,
          filename: 'project.tar.gz',
          artifact: {
            inlineData: {
              data: buf.toString('base64'),
              mimeType: 'application/gzip',
            },
          } as any,
        });
      }

      const port = process.env.PORT || '8001';
      const downloadUrl = `http://localhost:${port}/download/${taskId}/project.tar.gz`;

      yield createEvent({
        author: this.name,
        invocationId: context.invocationId,
        content: {
          parts: [
            {
              text: `Frontend project generated and verified successfully. Download: ${downloadUrl}`,
            },
          ],
        },
        actions: createEventActions({ artifactDelta: { 'project.tar.gz': 0 } }),
        timestamp: Date.now(),
      });
    } catch (err: any) {
      yield createEvent({
        author: this.name,
        invocationId: context.invocationId,
        content: {
          parts: [{ text: `Failed to generate frontend project: ${err.message}` }],
        },
        actions: createEventActions(),
        timestamp: Date.now(),
      });
    }
  }

  async *runLiveImpl(
    _context: InvocationContext,
  ): AsyncGenerator<ReturnType<typeof createEvent>, void, void> {
    throw new Error('Live mode is not supported by FrontendAgent.');
  }

  private statusEvent(context: InvocationContext, message: string) {
    return createEvent({
      author: this.name,
      invocationId: context.invocationId,
      content: { parts: [{ text: message }] },
      actions: createEventActions(),
      timestamp: Date.now(),
    });
  }
}
