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
    registerSelf();
  });
}

/** Register this agent into the Agent Registry (self-registration). */
async function registerSelf() {
  const registryUrl = process.env.AGENT_REGISTRY_URL;
  if (!registryUrl) return;
  const clientKey = process.env.REGISTRY_CLIENT_KEY || '';
  const serviceName = process.env.SERVICE_NAME || 'frontend_agent';
  const ownUrl = `http://${serviceName}:${port}`;
  const headers: Record<string, string> = { 'Content-Type': 'application/json' };
  if (clientKey) headers['X-Registry-Key'] = clientKey;
  try {
    const resp = await fetch(`${registryUrl.replace(/\/$/, '')}/agents`, {
      method: 'POST',
      headers,
      body: JSON.stringify({
        name: 'frontend_agent',
        url: ownUrl,
        description:
          'A specialist agent that builds runnable Vite + React frontend projects from a natural language description.',
        type: 'specialist',
      }),
    });
    if (resp.ok) {
      console.log(`[frontend_agent] registered self @ ${ownUrl}`);
    } else {
      console.log(`[frontend_agent] self-registration status ${resp.status}`);
    }
  } catch (e) {
    console.log(`[frontend_agent] self-registration failed (non-fatal): ${(e as Error).message}`);
  }
}

start().catch((err) => {
  console.error(err);
  process.exit(1);
});
