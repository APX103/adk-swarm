import { openai, MODEL } from './openai.js';

const SYSTEM_PROMPT = `You are an expert frontend engineer specializing in Vite + React.

Given a user request, generate a complete, runnable Vite + React JavaScript project.

Return ONLY a JSON object with the following structure (no markdown, no explanation):

{
  "files": [
    { "path": "package.json", "content": "{\\\"name\\\":\\\"vite-react-app\\\",...}" },
    { "path": "vite.config.js", "content": "..." },
    { "path": "index.html", "content": "..." },
    { "path": "src/main.jsx", "content": "..." },
    { "path": "src/App.jsx", "content": "..." },
    { "path": "src/index.css", "content": "..." }
  ]
}

Important: the "path" field must be the exact filename including its extension, for example "package.json".

Rules:
1. Use Vite 6 and React 18.
2. package.json must include scripts: { "dev": "vite", "build": "vite build", "preview": "vite preview" }.
3. package.json dependencies must include "react" and "react-dom".
4. package.json devDependencies must include "vite" and "@vitejs/plugin-react".
5. vite.config.js must import @vitejs/plugin-react and export default { plugins: [react()] }.
6. index.html must contain <div id=\\"root\\"></div> and <script type=\\"module\\" src=\\"/src/main.jsx\\"></script>.
7. src/main.jsx must import ReactDOM from 'react-dom/client', import App from './App.jsx', and render <App /> into document.getElementById('root').
8. src/App.jsx must be a default-exported functional component.
9. Do not include TODO, placeholder, or "implement later" comments. Provide real working code.
10. CSS should be plain and self-contained; avoid external CDNs.`;

export async function generateProjectFiles(userRequest: string): Promise<Record<string, string>> {
  const response = await openai.chat.completions.create({
    model: MODEL,
    messages: [
      { role: 'system', content: SYSTEM_PROMPT },
      { role: 'user', content: userRequest },
    ],
    temperature: 0.2,
    response_format: { type: 'json_object' },
  });

  const raw = response.choices[0]?.message?.content || '{}';
  console.log('[generator] Raw LLM response:', raw.slice(0, 800));
  const parsed = JSON.parse(raw);

  if (!Array.isArray(parsed.files)) {
    throw new Error('LLM did not return a valid "files" array.');
  }

  const files: Record<string, string> = {};
  for (const item of parsed.files) {
    if (item && typeof item.path === 'string' && typeof item.content === 'string') {
      files[item.path] = item.content;
    }
  }

  // Some models consistently drop the "json" extension from "package.json".
  if (files['package.'] && !files['package.json']) {
    files['package.json'] = files['package.'];
    delete files['package.'];
  }

  console.log('[generator] Generated file keys:', Object.keys(files));
  return files;
}
