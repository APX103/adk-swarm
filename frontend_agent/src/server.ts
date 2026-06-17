import 'dotenv/config';
import express from 'express';
import fs from 'fs/promises';
import path from 'path';
import { toA2a, FileArtifactService } from '@google/adk';
import { FrontendAgent } from './frontend_agent.js';

const port = parseInt(process.env.PORT || '8001');
const host = process.env.HOST || '0.0.0.0';
const artifactRoot = process.env.ARTIFACT_ROOT || '/workspace/artifacts';
const downloadRoot = '/workspace/downloads';

async function start() {
  const app = express();
  app.use(express.json());

  app.get('/health', (_req, res) => {
    res.json({ status: 'ok' });
  });

  app.get('/download/:taskId/project.tar.gz', async (req, res) => {
    const filePath = path.join(downloadRoot, req.params.taskId, 'project.tar.gz');
    try {
      await fs.access(filePath);
      res.sendFile(filePath);
    } catch {
      res.status(404).send('Archive not found');
    }
  });

  const agent = new FrontendAgent({
    name: 'frontend_agent',
    description:
      'A specialist agent that builds runnable Vite + React frontend projects from a natural language description.',
  });

  const artifactService = new FileArtifactService(artifactRoot);
  await toA2a(agent, {
    app,
    host,
    port,
    protocol: 'http',
    basePath: '',
    artifactService,
  });

  app.listen(port, host, () => {
    console.log(`Frontend agent A2A server listening on http://${host}:${port}`);
    console.log(`Agent card: http://${host}:${port}/.well-known/agent-card.json`);
  });
}

start().catch((err) => {
  console.error(err);
  process.exit(1);
});
